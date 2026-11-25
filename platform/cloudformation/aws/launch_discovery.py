"""Resolve the AWS resources selected on the first-launch console form."""

import hashlib
import logging
import os
import re


log = logging.getLogger(__name__)
DNS_NAME = re.compile(r"(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")
PRIVATE_ZONE_WAIT_SECONDS = 300
PRIVATE_ZONE_POLL_SECONDS = 10


def public_candidates(ec2, vpc, zone):
    """Find available subnets with a direct, active IPv4 route to the Internet."""
    tables = ec2.get_paginator("describe_route_tables").paginate(Filters=[{"Name": "vpc-id", "Values": [vpc]}])
    public_by_subnet = {}
    main_is_public = False
    for page in tables:
        for table in page["RouteTables"]:
            is_public = any(route.get("DestinationCidrBlock") == "0.0.0.0/0"
                            and route.get("State") == "active"
                            and route.get("GatewayId", "").startswith("igw-") for route in table["Routes"])
            for association in table["Associations"]:
                if association.get("Main"):
                    main_is_public = is_public
                if "SubnetId" in association:
                    public_by_subnet[association["SubnetId"]] = is_public
    filters = [{"Name": "vpc-id", "Values": [vpc]}, {"Name": "availability-zone", "Values": [zone]},
               {"Name": "state", "Values": ["available"]}]
    pages = ec2.get_paginator("describe_subnets").paginate(Filters=filters)
    return sorted((subnet for page in pages for subnet in page["Subnets"]
                   if subnet["VpcId"] == vpc and subnet["AvailabilityZone"] == zone
                   and subnet["State"] == "available" and subnet["AvailableIpAddressCount"] >= 8
                   and public_by_subnet.get(subnet["SubnetId"], main_is_public)), key=lambda subnet: subnet["SubnetId"])


def resolve_launch(props, ec2, dns, *, previous_public_subnet=""):
    name = props["DeploymentName"]
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,30}[a-z0-9]", name):
        raise ValueError("Use a stack name of 2–32 lowercase letters, numbers, or hyphens, starting with a letter and ending with a letter or number.")
    ids = [props["ServiceSubnet"], props["WorkspaceSubnet"]]
    subnets = {subnet["SubnetId"]: subnet for subnet in ec2.describe_subnets(SubnetIds=sorted(set(ids)))["Subnets"]}
    if any(subnet_id not in subnets or subnets[subnet_id]["State"] != "available" for subnet_id in ids):
        raise ValueError("Choose available service and workspace subnets.")
    service, workspace = (subnets[subnet_id] for subnet_id in ids)
    vpc = service["VpcId"]
    zone = service["AvailabilityZone"]
    if workspace["VpcId"] != vpc:
        raise ValueError("Choose service and workspace subnets in the same VPC.")
    suffix = props.get("DnsSuffix") or f"{name}.internal"
    if not DNS_NAME.fullmatch(suffix):
        raise ValueError("Enter a valid lowercase private DNS suffix.")

    domain = ""
    if props.get("PublicZone"):
        hosted_zone = dns.get_hosted_zone(Id=props["PublicZone"])["HostedZone"]
        if hosted_zone.get("Config", {}).get("PrivateZone"):
            raise ValueError("Choose an authoritative public Route 53 hosted zone.")
        domain = hosted_zone["Name"].rstrip(".")
        if len(domain) > 160 or not DNS_NAME.fullmatch(domain) or suffix == domain:
            raise ValueError("The public zone must have a supported DNS name distinct from the private DNS suffix.")

    public_subnet = ""
    if props.get("PublicAccess") == "true":
        requested = props.get("PublicSubnet")
        if previous_public_subnet and (not requested or requested == previous_public_subnet):
            public_subnet = previous_public_subnet
        else:
            available = [subnet["SubnetId"] for subnet in public_candidates(ec2, vpc, zone)]
            if requested and requested not in available:
                raise ValueError(f"Select an eligible public subnet with eight free IPv4 addresses in {vpc} and {zone}.")
            if not requested and len(available) != 1:
                if not available:
                    raise ValueError(f"No eligible public subnet with eight free IPv4 addresses in {vpc} and {zone}.")
                raise ValueError("Choose public subnet with the explicit placement form. Eligible subnets: " + ", ".join(available))
            public_subnet = requested or available[0]
    return {"VpcId": vpc, "AvailabilityZone": zone, "PublicSubnet": public_subnet,
            "BaseDomain": domain, "DnsSuffix": suffix}


def resolve_private_zone(props, dns, region, account, token):
    vpc = props["VpcId"]
    domain = props["BaseDomain"]
    query = {"VPCId": vpc, "VPCRegion": region}
    candidates = []
    while True:
        page = dns.list_hosted_zones_by_vpc(**query)
        candidates.extend(zone for zone in page["HostedZoneSummaries"] if zone["Name"].rstrip(".") == domain)
        if not page.get("NextToken"):
            break
        query["NextToken"] = page["NextToken"]
    if len(candidates) > 1:
        raise ValueError("The VPC has conflicting private hosted zones for the selected domain.")
    change_id = None
    if candidates:
        zone = candidates[0]
        if zone["Owner"].get("OwningAccount") != account:
            raise ValueError("The matching private hosted zone is owned by another account or AWS service.")
        zone_id = zone["HostedZoneId"].rsplit("/", 1)[-1]
        owned = dns.get_hosted_zone(Id=zone_id)["HostedZone"].get("CallerReference") == token
    else:
        created = dns.create_hosted_zone(Name=domain, CallerReference=token, VPC={"VPCId": vpc, "VPCRegion": region},
                                         HostedZoneConfig={"Comment": "ShiftBox private browser DNS"})
        zone_id, owned = created["HostedZone"]["Id"].rsplit("/", 1)[-1], True
        if created["ChangeInfo"]["Status"] != "INSYNC":
            change_id = created["ChangeInfo"]["Id"]
    return zone_id, owned, change_id


def delete_private_zone(dns, zone_id, token):
    """Remove an empty zone this stack created. Preserve adopted and shared zones."""
    try:
        zone = dns.get_hosted_zone(Id=zone_id)["HostedZone"]
        if zone.get("CallerReference") == token and zone["Config"]["PrivateZone"]:
            dns.delete_hosted_zone(Id=zone_id)
    except dns.exceptions.NoSuchHostedZone:
        return
    except dns.exceptions.HostedZoneNotEmpty:
        log.info("Preserving private hosted zone %s because other DNS records still use it", zone_id)


def wait_for_private_zone(dns, change_id):
    from botocore.exceptions import WaiterError

    try:
        dns.get_waiter("resource_record_sets_changed").wait(
            Id=change_id,
            WaiterConfig={"Delay": PRIVATE_ZONE_POLL_SECONDS,
                          "MaxAttempts": PRIVATE_ZONE_WAIT_SECONDS // PRIVATE_ZONE_POLL_SECONDS + 1},
        )
    except WaiterError as error:
        last = error.last_response
        if last.get("ChangeInfo", {}).get("Status") == "PENDING":
            raise ValueError("Route 53 was still propagating the private hosted zone after five minutes.") from error
        code = last.get("Error", {}).get("Code", "unknown status")
        raise ValueError(f"Route 53 could not verify the private hosted zone ({code}).") from error


def respond_to_cloudformation(event, context, cfnresponse):
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError

    kind = event["ResourceType"]
    request = event["RequestType"]
    props = event["ResourceProperties"]
    token = hashlib.sha256((event["StackId"] + event["LogicalResourceId"]).encode()).hexdigest()
    physical_id = event.get("PhysicalResourceId", token)
    data = {}
    status, reason = cfnresponse.SUCCESS, None
    config = Config(connect_timeout=3, read_timeout=5, retries={"total_max_attempts": 3, "mode": "standard"})
    try:
        if kind == "Custom::ShiftBoxPrivateBrowserZone":
            dns = boto3.client("route53", config=config)
            if request == "Delete":
                if physical_id.startswith("owned:"):
                    delete_private_zone(dns, physical_id.removeprefix("owned:"), token)
            else:
                old = event.get("OldResourceProperties", props)
                if any(props[field] != old[field] for field in ("VpcId", "BaseDomain")):
                    raise ValueError("Keep the installed VPC and public zone unchanged.")
                account = event["StackId"].split(":")[4]
                zone_id, owned, change_id = resolve_private_zone(props, dns, os.environ["AWS_REGION"], account, token)
                # Report the owned physical ID even if tagging or propagation fails.
                physical_id = ("owned:" if owned else "adopted:") + zone_id
                if change_id:
                    wait_for_private_zone(dns, change_id)
                if owned:
                    dns.change_tags_for_resource(ResourceType="hostedzone", ResourceId=zone_id, AddTags=[
                        {"Key": "shiftbox-project", "Value": "shiftbox"},
                        {"Key": "shiftbox-cluster-name", "Value": props["DeploymentName"]},
                    ])
                data = {"ZoneId": zone_id}
        elif request != "Delete":
            old = event.get("OldResourceProperties", props)
            for field in ("ServiceSubnet", "PublicZone", "DnsSuffix"):
                if props.get(field) != old.get(field):
                    raise ValueError("Keep the installed service subnet, public zone, and private DNS suffix unchanged.")
            old_token, _, previous_subnet = physical_id.partition(":")
            if old_token != token:
                previous_subnet = ""
            data = resolve_launch(props, boto3.client("ec2", config=config), boto3.client("route53", config=config),
                                  previous_public_subnet=previous_subnet)
            selected = data["PublicSubnet"] or previous_subnet
            physical_id = f"{token}:{selected}" if selected else token
    except ValueError as error:
        status, reason = cfnresponse.FAILED, str(error)
    except ClientError as error:
        code = error.response["Error"]["Code"]
        status, reason = cfnresponse.FAILED, f"AWS launch discovery failed during {error.operation_name} ({code})."
    except Exception:
        log.exception("AWS launch discovery failed")
        status, reason = cfnresponse.FAILED, "Launch discovery failed. Check the discovery function logs."
    cfnresponse.send(event, context, status, data, physicalResourceId=physical_id, reason=reason)
