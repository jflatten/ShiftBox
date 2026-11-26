"""Test single-create discovery with AWS API doubles."""

from pathlib import Path
import sys
from types import ModuleType

import pytest
import yaml


@pytest.fixture(scope="module")
def resources():
    # Exercise the same inline Lambda that operators get in the checked-in template.
    template = Path(__file__).with_name("template.yaml")
    return yaml.safe_load(template.read_text())["Resources"]


@pytest.fixture
def discovery(resources, monkeypatch):
    code = resources["DiscoveryFunction"]["Properties"]["Code"]["ZipFile"]
    monkeypatch.setitem(sys.modules, "cfnresponse", ModuleType("cfnresponse"))
    namespace = {}
    exec(compile(code, "<CloudFormation discovery Lambda>", "exec"), namespace)
    return namespace


class EC2:
    def __init__(self, public_ids=("subnet-public",)):
        self.subnets = [self.subnet("subnet-service"), self.subnet("subnet-workspace"),
                        self.subnet("subnet-other", zone="us-east-2b")]
        self.subnets.extend(self.subnet(subnet_id) for subnet_id in public_ids)
        self.public_ids = public_ids

    @staticmethod
    def subnet(subnet_id, zone="us-east-2a", vpc="vpc-example"):
        return {"SubnetId": subnet_id, "VpcId": vpc, "AvailabilityZone": zone,
                "AvailableIpAddressCount": 12, "State": "available"}

    def describe_subnets(self, SubnetIds):
        return {"Subnets": [s for s in self.subnets if s["SubnetId"] in SubnetIds]}

    def describe_route_tables(self, **_):
        raise AssertionError("Use the paginator")

    def get_paginator(self, operation):
        class Pages:
            def paginate(pages, **_):
                if operation == "describe_subnets":
                    yield {"Subnets": self.subnets}
                elif operation == "describe_route_tables":
                    yield {"RouteTables": [
                        {"Associations": [{"Main": True}], "Routes": []},
                        {"Associations": [{"SubnetId": subnet_id} for subnet_id in self.public_ids],
                         "Routes": [{"DestinationCidrBlock": "0.0.0.0/0", "GatewayId": "igw-example", "State": "active"}]},
                    ]}
                else:
                    raise AssertionError(operation)
        return Pages()


class DNS:
    def __init__(self, zones=()):
        self.zones = list(zones)
        self.created = []

    def get_hosted_zone(self, Id):
        return {"HostedZone": {"Id": Id, "Name": "shiftbox.dev.", "Config": {"PrivateZone": False}}}

    def list_hosted_zones_by_vpc(self, **_):
        return {"HostedZoneSummaries": self.zones}

    def create_hosted_zone(self, **kwargs):
        self.created.append(kwargs)
        return {"HostedZone": {"Id": "/hostedzone/ZCREATED"}, "ChangeInfo": {"Status": "INSYNC"}}

@pytest.fixture
def inputs():
    return {"ServiceSubnet": "subnet-service", "WorkspaceSubnet": "subnet-workspace",
            "DeploymentName": "rc3", "PublicZone": "ZPUBLIC", "PublicAccess": "true"}


def test_auto_public_launch(inputs, discovery):
    # One Internet-routed subnet is enough to derive all the missing launch values.
    assert discovery["resolve_launch"](inputs, EC2(), DNS()) == {
        "VpcId": "vpc-example", "AvailabilityZone": "us-east-2a", "PublicSubnet": "subnet-public",
        "BaseDomain": "shiftbox.dev", "DnsSuffix": "rc3.internal",
    }


def test_ambiguous_public_subnets_fail_closed(inputs, discovery):
    # Multiple eligible subnets must not change the load balancer placement arbitrarily.
    ec2 = EC2(("subnet-public", "subnet-second"))
    with pytest.raises(ValueError, match="More than one eligible public subnet"):
        discovery["resolve_launch"](inputs, ec2, DNS())


def test_workspace_vpc_mismatch_and_public_cutover(inputs, discovery):
    # Cutover can keep managed DNS without any public subnet, but workspace VPC validation stays active.
    inputs["PublicAccess"] = "false"
    ec2 = EC2(())
    resolve_launch = discovery["resolve_launch"]
    assert resolve_launch(inputs, ec2, DNS())["PublicSubnet"] == ""
    ec2.subnets[1]["VpcId"] = "vpc-other"
    with pytest.raises(ValueError, match="same VPC"):
        resolve_launch(inputs, ec2, DNS())


def test_updates_keep_chosen_public_subnet(inputs, discovery):
    # A new free subnet or a lower remaining IP count must not move an existing NLB.
    ec2 = EC2(())
    resolve_launch = discovery["resolve_launch"]
    assert resolve_launch(inputs, ec2, DNS(), previous_public_subnet="subnet-original")["PublicSubnet"] == "subnet-original"
    inputs["PublicAccess"] = "false"
    assert resolve_launch(inputs, ec2, DNS(), previous_public_subnet="subnet-original")["PublicSubnet"] == ""


def test_private_zone_reuse_and_create(discovery):
    # A matching account-owned VPC zone is reused, while a missing zone is created with a stable caller reference.
    zone = {"HostedZoneId": "ZEXISTING", "Name": "shiftbox.dev.", "Owner": {"OwningAccount": "123456789012"}}
    props = {"VpcId": "vpc-example", "BaseDomain": "shiftbox.dev", "DeploymentName": "rc3"}
    resolve_private_zone = discovery["resolve_private_zone"]
    dns = DNS([zone])
    assert resolve_private_zone(props, dns, "us-east-2", "123456789012", "stack-token") == ("ZEXISTING", False, None)
    assert not dns.created
    dns = DNS()
    assert resolve_private_zone(props, dns, "us-east-2", "123456789012", "stack-token") == ("ZCREATED", True, None)
    assert dns.created[0]["CallerReference"] == "stack-token"
    assert dns.created[0]["VPC"]["VPCId"] == "vpc-example"
    zone["Owner"]["OwningAccount"] = "999999999999"
    with pytest.raises(ValueError, match="another account"):
        resolve_private_zone(props, DNS([zone]), "us-east-2", "123456789012", "stack-token")


def test_delete_does_not_remove_an_adopted_or_nonempty_zone(discovery):
    # Ownership and surviving record checks protect zones shared by other installations.
    class DeleteDNS(DNS):
        class exceptions:
            class NoSuchHostedZone(Exception):
                pass

            class HostedZoneNotEmpty(Exception):
                pass

        def __init__(self, token, empty):
            super().__init__()
            self.token, self.empty, self.deleted = token, empty, False

        def get_hosted_zone(self, Id):
            return {"HostedZone": {"CallerReference": self.token, "Config": {"PrivateZone": True}}}

        def delete_hosted_zone(self, Id):
            if not self.empty:
                raise self.exceptions.HostedZoneNotEmpty()
            self.deleted = True

    for token, empty, removed in (("ours", True, True), ("theirs", True, False), ("ours", False, False)):
        dns = DeleteDNS(token, empty)
        discovery["delete_private_zone"](dns, "ZPRIVATE", "ours")
        assert dns.deleted is removed


def test_private_zone_can_propagate_past_the_old_one_minute_limit(monkeypatch, discovery):
    # Exercise the real SDK waiter with a zone that remains pending for 90 seconds.
    from botocore.session import get_session
    from botocore.stub import Stubber

    client = get_session().create_client("route53", aws_access_key_id="testing", aws_secret_access_key="testing")
    pauses = []
    monkeypatch.setattr("botocore.waiter.time.sleep", pauses.append)
    change_id = "/change/CEXAMPLE"
    params = {"Id": change_id}
    with Stubber(client) as stub:
        for status in (["PENDING"] * 9) + ["INSYNC"]:
            stub.add_response("get_change", {"ChangeInfo": {"Id": change_id, "Status": status,
                                                            "SubmittedAt": "2026-11-25T16:00:00Z"}}, params)
        discovery["wait_for_private_zone"](client, change_id)
        stub.assert_no_pending_responses()
    assert sum(pauses) == 90


def test_private_zone_has_time_to_report_a_propagation_timeout(resources):
    # CloudFormation must allow enough time for the Lambda to report its own limit.
    assert resources["DiscoveryFunction"]["Properties"]["Timeout"] >= 360
    assert int(resources["PrivateBrowserZone"]["Properties"]["ServiceTimeout"]) > resources["DiscoveryFunction"]["Properties"]["Timeout"]


def test_private_zone_ec2_permissions_are_in_policy_and_boundary(resources):
    # Route 53 checks EC2 read permissions when listing or creating private
    # zones for a VPC. Both the identity policy and its boundary must allow them.
    role_policy = resources["DiscoveryRole"]["Properties"]["Policies"][0]["PolicyDocument"]
    boundary = resources["DiscoveryBoundary"]["Properties"]["PolicyDocument"]
    assert role_policy == boundary
    dependent_actions = {"ec2:DescribeVpcs", "ec2:DescribeRegions"}
    matching_statements = [statement for statement in role_policy["Statement"]
                           if dependent_actions.intersection(statement["Action"]
                                                            if isinstance(statement["Action"], list)
                                                            else [statement["Action"]])]
    assert matching_statements == [{"Effect": "Allow", "Resource": "*", "Action": sorted(dependent_actions)}]
