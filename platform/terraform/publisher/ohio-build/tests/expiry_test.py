from datetime import datetime, timezone
from pathlib import Path
import sys

import pytest
from botocore.exceptions import ClientError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from expiry import Attempt, sweep  # noqa: E402


class Pages:
    def __init__(self, responses):
        self.responses = responses

    def paginate(self, **request):
        return iter(self.responses(**request))


class FakeEC2:
    def __init__(self, attempt):
        self.attempt = attempt
        self.writes = []
        self.nat_state = "available"
        self.nat_scope = attempt.egress_subnet_id
        self.associated = True
        self.is_default = False
        self.builder_scope = attempt.subnet_id
        self.managed_interface = False
        self.attempt_builder_tag = True
        self.attempt_eni_tag = True
        self.attempt_volume_tag = True
        self.builder_state = "running"
        self.reject_termination = False

    def tagged(self, exact=False):
        source = self.attempt.tags
        return [{"Key": key, "Value": value} for key, value in source.items()
                if exact or key != "publisher_attempt"]

    def describe_vpcs(self, **request):
        return {"Vpcs": [{"VpcId": self.attempt.vpc_id, "OwnerId": self.attempt.account,
                          "IsDefault": self.is_default, "Tags": self.tagged(True)}]}

    def describe_subnets(self, **request):
        return {"Subnets": [{"SubnetId": self.attempt.subnet_id, "OwnerId": self.attempt.account,
                             "VpcId": self.attempt.vpc_id, "AvailabilityZone": self.attempt.zone,
                             "Tags": self.tagged(True)}]}

    def describe_instances(self, **request):
        builder = {
            "InstanceId": "i-00000000000000022",
            "InstanceType": "t3.medium",
            "State": {"Name": self.builder_state},
            "VpcId": self.attempt.vpc_id,
            "SubnetId": self.builder_scope,
            "SecurityGroups": [{"GroupId": self.attempt.builder_group}],
            "Tags": self.tagged(self.attempt_builder_tag),
        }
        return [{"Reservations": [{"OwnerId": self.attempt.account, "Instances": [builder]}]}]

    def describe_nat_gateways(self, **request):
        return {"NatGateways": [{
            "NatGatewayId": self.attempt.nat_id,
            "VpcId": self.attempt.vpc_id,
            "State": self.nat_state,
            "SubnetId": self.nat_scope,
            "ConnectivityType": "public",
            "Tags": self.tagged(True),
            "NatGatewayAddresses": [{"AllocationId": self.attempt.allocation_id}],
        }]}

    def describe_addresses(self, **request):
        return {"Addresses": [{
            "AllocationId": self.attempt.allocation_id,
            "Domain": "vpc",
            **({"AssociationId": "eipassoc-00000000000000001"} if self.associated else {}),
            "Tags": self.tagged(True),
        }]}

    def describe_network_interfaces(self, **request):
        return [{"NetworkInterfaces": [{
            "NetworkInterfaceId": "eni-00000000000000088",
            "VpcId": self.attempt.vpc_id,
            "SubnetId": self.attempt.subnet_id,
            "OwnerId": self.attempt.account,
            "Status": "available",
            "InterfaceType": "interface",
            "RequesterManaged": self.managed_interface,
            "Groups": [{"GroupId": self.attempt.builder_group}],
            "TagSet": self.tagged(self.attempt_eni_tag),
        }]}]

    def describe_volumes(self, **request):
        return [{"Volumes": [{
            "VolumeId": "vol-00000000000000021",
            "State": "available",
            "AvailabilityZone": self.attempt.zone,
            "Attachments": [],
            "Tags": self.tagged(self.attempt_volume_tag),
        }]}]

    def get_paginator(self, operation):
        return Pages(getattr(self, operation))

    def terminate_instances(self, *, InstanceIds):
        if self.reject_termination:
            raise ClientError({"Error": {"Code": "RequestLimitExceeded"}}, "TerminateInstances")
        self.writes.append(("terminate", tuple(InstanceIds)))

    def delete_nat_gateway(self, *, NatGatewayId):
        self.writes.append(("nat", NatGatewayId))

    def release_address(self, *, AllocationId):
        self.writes.append(("eip", AllocationId))

    def delete_network_interface(self, *, NetworkInterfaceId):
        self.writes.append(("eni", NetworkInterfaceId))

    def delete_volume(self, *, VolumeId):
        self.writes.append(("volume", VolumeId))


@pytest.fixture
def attempt():
    return Attempt(
        account="111122223333",
        region="us-east-2",
        attempt_id="review-123",
        candidate_tag="v1.0.0-rc.1",
        expires_at=datetime(2026, 11, 22, 10, tzinfo=timezone.utc),
        vpc_id="vpc-00000000000000011",
        subnet_id="subnet-00000000000000012",
        builder_group="sg-00000000000000013",
        egress_subnet_id="subnet-00000000000000023",
        zone="us-east-2a",
        nat_id="nat-00000000000000051",
        allocation_id="eipalloc-00000000000000061",
    )


def invoke(client, attempt, minute=1, account="111122223333"):
    return sweep(client, attempt, account, attempt.attempt_id,
                 datetime(2026, 11, 22, 10, minute, tzinfo=timezone.utc))


@pytest.mark.parametrize("account,minute", [("444455556666", 1), ("111122223333", 40)])
def test_rejects_wrong_account_or_outside_cleanup_window(attempt, account, minute):
    # Wrong-account and late deliveries must never mutate anything.
    ec2 = FakeEC2(attempt)
    with pytest.raises(ValueError):
        invoke(ec2, attempt, minute, account)
    assert ec2.writes == []


def test_rejects_early_event(attempt):
    # A manual invocation may not stop an active, unexpired attempt.
    ec2 = FakeEC2(attempt)
    with pytest.raises(ValueError):
        sweep(ec2, attempt, attempt.account, attempt.attempt_id,
              datetime(2026, 11, 22, 9, 59, tzinfo=timezone.utc))
    assert ec2.writes == []


def test_rejects_default_network(attempt):
    # Even matching tag fixtures cannot make the default VPC eligible.
    ec2 = FakeEC2(attempt)
    ec2.is_default = True
    with pytest.raises(ValueError):
        invoke(ec2, attempt)
    assert ec2.writes == []


def test_expiry_stops_compute_and_nat_but_defers_associated_address(attempt):
    # Stop billable resources first, and let AWS finish NAT removal before EIP release.
    ec2 = FakeEC2(attempt)
    pending = invoke(ec2, attempt)
    assert ("terminate", ("i-00000000000000022",)) in ec2.writes
    assert ("nat", attempt.nat_id) in ec2.writes
    assert ("eip", attempt.allocation_id) not in ec2.writes
    assert ("volume", "vol-00000000000000021") in ec2.writes
    assert ("eni", "eni-00000000000000088") in ec2.writes
    assert "nat" in pending and "eip" in pending


def test_expiry_releases_exact_disassociated_address_after_nat(attempt):
    # A later invocation can release only the same Terraform state EIP.
    ec2 = FakeEC2(attempt)
    ec2.nat_state = "deleted"
    ec2.associated = False
    ec2.builder_state = "terminated"
    pending = invoke(ec2, attempt)
    assert ("terminate", ("i-00000000000000022",)) not in ec2.writes
    assert ("nat", attempt.nat_id) not in ec2.writes
    assert ("eip", attempt.allocation_id) in ec2.writes
    assert "nat" not in pending and "eip" not in pending


def test_ignores_unproven_dynamic_resources(attempt):
    # A foreign subnet, managed ENI, or volume without attempt ownership survives cleanup.
    ec2 = FakeEC2(attempt)
    ec2.builder_scope = "subnet-00000000000000091"
    ec2.managed_interface = True
    ec2.attempt_volume_tag = False
    invoke(ec2, attempt)
    assert not any(action == "terminate" for action, _ in ec2.writes)
    assert not any(action in {"eni", "volume"} for action, _ in ec2.writes)


def test_shutdown_and_cloud_failure_are_independent(attempt):
    # An incomplete compute shutdown must be reported without blocking NAT deletion.
    ec2 = FakeEC2(attempt)
    ec2.reject_termination = True
    pending = invoke(ec2, attempt)
    assert "instances" in pending
    assert ("nat", attempt.nat_id) in ec2.writes


def test_rejects_misplaced_nat(attempt):
    # Even a matching NAT ID and tags cannot authorize deletion outside the egress subnet.
    ec2 = FakeEC2(attempt)
    ec2.nat_scope = attempt.subnet_id
    with pytest.raises(ValueError):
        invoke(ec2, attempt)
    assert ec2.writes == []


def test_requires_attempt_tags_on_every_dynamic_resource(attempt):
    # Correct project and candidate tags alone do not establish ownership.
    ec2 = FakeEC2(attempt)
    ec2.attempt_builder_tag = False
    ec2.attempt_eni_tag = False
    ec2.attempt_volume_tag = False
    invoke(ec2, attempt)
    assert not any(action in {"terminate", "eni", "volume"} for action, _ in ec2.writes)
