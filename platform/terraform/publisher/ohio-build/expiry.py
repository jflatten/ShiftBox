"""Stop a single expired ShiftBox publisher attempt without deleting its AMIs."""

from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
import logging
import os

from botocore.exceptions import BotoCoreError, ClientError

REGION = "us-east-2"
WINDOW = timedelta(minutes=35)
LOG = logging.getLogger(__name__)
LOG.setLevel(logging.INFO)


@dataclass(frozen=True)
class Attempt:
    account: str
    region: str
    attempt_id: str
    candidate_tag: str
    expires_at: datetime
    vpc_id: str
    subnet_id: str
    builder_group: str
    egress_subnet_id: str
    zone: str
    nat_id: str
    allocation_id: str

    @property
    def tags(self):
        return {
            "project": "shiftbox",
            "cluster_name": "shiftbox-publisher",
            "environment": f"release-{self.candidate_tag}",
            "publisher_attempt": self.attempt_id,
        }


def tag_filters(tags):
    return [{"Name": f"tag:{key}", "Values": [value]} for key, value in tags.items()]


def filters(**values):
    return [{"Name": key.replace("_", "-"), "Values": [value]} for key, value in values.items()]


def matching_tags(resource, tags, *, field="Tags"):
    observed = {tag["Key"]: tag["Value"] for tag in resource.get(field, [])}
    return all(observed.get(key) == value for key, value in tags.items())


def one(items, description):
    if len(items) > 1:
        raise ValueError(f"ambiguous {description}")
    return items[0] if items else None


def instances(ec2, resource_filters):
    for page in ec2.get_paginator("describe_instances").paginate(Filters=resource_filters):
        for reservation in page["Reservations"]:
            for instance in reservation["Instances"]:
                yield (reservation["OwnerId"], instance)


def eligible_location(resource, attempt):
    return resource["VpcId"] == attempt.vpc_id and resource["SubnetId"] == attempt.subnet_id


def attempt_cleanup(ec2, action, **request):
    try:
        getattr(ec2, action)(**request)
    except (ClientError, BotoCoreError) as error:
        # Preserve unrelated actions and report the failed category for a later pass.
        LOG.warning("Cleanup action %s failed (%s)", action, type(error).__name__)
        return False
    return True


def sweep(ec2, attempt, account, event_attempt, now):
    if (
        account != attempt.account or attempt.region != REGION
        or event_attempt != attempt.attempt_id
        or not attempt.expires_at <= now < attempt.expires_at + WINDOW
    ):
        raise ValueError("cleanup requires the publisher account and an expired matching attempt")

    tags = attempt.tags
    private_scope = filters(vpc_id=attempt.vpc_id, subnet_id=attempt.subnet_id)
    vpc = one(ec2.describe_vpcs(Filters=filters(vpc_id=attempt.vpc_id))["Vpcs"], "VPC")
    if (
        not vpc or vpc["VpcId"] != attempt.vpc_id or vpc["OwnerId"] != account
        or vpc["IsDefault"] is not False or not matching_tags(vpc, tags)
    ):
        raise ValueError("cleanup could not verify its dedicated VPC")

    subnet = one(
        ec2.describe_subnets(Filters=filters(subnet_id=attempt.subnet_id))["Subnets"], "subnet"
    )
    if (
        not subnet or not eligible_location(subnet, attempt) or subnet["OwnerId"] != account
        or subnet["AvailabilityZone"] != attempt.zone or not matching_tags(subnet, tags)
    ):
        raise ValueError("cleanup could not verify its dedicated private subnet")

    terminate = []
    pending = set()
    builders = instances(
        ec2, private_scope + filters(group_id=attempt.builder_group) + tag_filters(tags)
    )
    for owner, instance in builders:
        if (
            owner == account
            and eligible_location(instance, attempt)
            and instance["InstanceType"] == "t3.medium"
            and {group["GroupId"] for group in instance["SecurityGroups"]} == {attempt.builder_group}
            and matching_tags(instance, tags)
        ):
            state = instance["State"]["Name"]
            if state != "terminated":
                pending.add("instances")
            if state not in {"terminated", "shutting-down"}:
                terminate.append(instance["InstanceId"])

    nat = one(ec2.describe_nat_gateways(Filter=filters(nat_gateway_id=attempt.nat_id))["NatGateways"], "NAT")
    if nat and (
        nat["NatGatewayId"] != attempt.nat_id or nat["VpcId"] != attempt.vpc_id
        or nat["SubnetId"] != attempt.egress_subnet_id or nat["ConnectivityType"] != "public" or not matching_tags(nat, tags)
        or any(address.get("AllocationId") != attempt.allocation_id
               for address in nat.get("NatGatewayAddresses", []))
    ):
        raise ValueError("cleanup could not verify its NAT gateway")

    address = one(ec2.describe_addresses(Filters=filters(allocation_id=attempt.allocation_id))["Addresses"], "EIP")
    if address and (
        address["AllocationId"] != attempt.allocation_id or address["Domain"] != "vpc"
        or not matching_tags(address, tags)
    ):
        raise ValueError("cleanup could not verify its Elastic IP")

    if terminate:
        attempt_cleanup(ec2, "terminate_instances", InstanceIds=terminate)
    if nat and nat["State"] != "deleted":
        pending.add("nat")
        if nat["State"] in {"pending", "available"}:
            attempt_cleanup(ec2, "delete_nat_gateway", NatGatewayId=attempt.nat_id)
    if address:
        if "nat" in pending or address.get("AssociationId") or address.get("NetworkInterfaceId"):
            pending.add("eip")
        elif not attempt_cleanup(ec2, "release_address", AllocationId=attempt.allocation_id):
            pending.add("eip")

    nic_filters = private_scope + filters(group_id=attempt.builder_group) + tag_filters(tags)
    for page in ec2.get_paginator("describe_network_interfaces").paginate(Filters=nic_filters):
        for interface in page["NetworkInterfaces"]:
            if (
                interface["OwnerId"] != account or not eligible_location(interface, attempt)
                or interface["InterfaceType"] != "interface" or interface["RequesterManaged"] is not False
                or {group["GroupId"] for group in interface["Groups"]} != {attempt.builder_group}
                or not matching_tags(interface, tags, field="TagSet")
            ):
                continue
            if interface["Status"] == "available" and not interface.get("Attachment"):
                if not attempt_cleanup(ec2, "delete_network_interface",
                                       NetworkInterfaceId=interface["NetworkInterfaceId"]):
                    pending.add("interfaces")
            else:
                pending.add("interfaces")

    # A detached EBS volume has no VPC/subnet. The attempt tag is mandatory.
    volume_filters = filters(availability_zone=attempt.zone) + tag_filters(tags)
    for page in ec2.get_paginator("describe_volumes").paginate(Filters=volume_filters):
        for volume in page["Volumes"]:
            if volume["AvailabilityZone"] != attempt.zone or not matching_tags(volume, tags):
                continue
            if volume["State"] == "available" and not volume["Attachments"]:
                if not attempt_cleanup(ec2, "delete_volume", VolumeId=volume["VolumeId"]):
                    pending.add("volumes")
            else:
                pending.add("volumes")
    return sorted(pending)


def handler(event, context):
    import boto3
    from botocore.config import Config

    attempt = Attempt(
        account=os.environ["PUBLISHER_ACCOUNT"],
        region=os.environ["AWS_REGION"],
        attempt_id=os.environ["ATTEMPT_ID"],
        candidate_tag=os.environ["CANDIDATE_TAG"],
        expires_at=datetime.fromisoformat(os.environ["EXPIRES_AT"]),
        vpc_id=os.environ["VPC_ID"],
        subnet_id=os.environ["PRIVATE_SUBNET_ID"],
        builder_group=os.environ["BUILDER_SECURITY_GROUP_ID"],
        egress_subnet_id=os.environ["EGRESS_SUBNET_ID"],
        zone=os.environ["AVAILABILITY_ZONE"],
        nat_id=os.environ["NAT_ID"],
        allocation_id=os.environ["ALLOCATION_ID"],
    )
    if attempt.region != REGION or context.invoked_function_arn != os.environ["EXPIRY_FUNCTION_ARN"]:
        raise ValueError("cleanup invoked outside its exact function or region")

    config = Config(connect_timeout=3, read_timeout=8, retries={"mode": "standard", "total_max_attempts": 3})
    sdk = boto3.Session(region_name=attempt.region)
    account = sdk.client("sts", config=config).get_caller_identity()["Account"]
    pending = sweep(
        sdk.client("ec2", config=config), attempt, account, event["attempt"], datetime.now(timezone.utc)
    )
    LOG.info("Expiry sweep remaining categories: %s", ",".join(pending) or "none")
    if event.get("final") is True and pending:
        raise RuntimeError("final expiry sweep could not verify complete billable cleanup")
    return {"pending": pending}
