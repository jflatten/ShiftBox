#!/usr/bin/env python3
"""Compare packaged CloudFormation resources with the actual execution policy draft."""

import json
from pathlib import Path
import subprocess
import sys

import yaml


class CloudFormationLoader(yaml.SafeLoader):
    """Preserve the intrinsic expressions needed to evaluate the qualification lane."""


def intrinsic(loader, tag, node):
    """Parse CloudFormation shorthand without rewriting ordinary YAML."""
    if isinstance(node, yaml.SequenceNode):
        return {tag: loader.construct_sequence(node, deep=True)}
    if isinstance(node, yaml.MappingNode):
        return {tag: loader.construct_mapping(node, deep=True)}
    return {tag: loader.construct_scalar(node)}


CloudFormationLoader.add_multi_constructor("!", intrinsic)
template = yaml.load(Path(sys.argv[1]).read_text(), Loader=CloudFormationLoader)
resources = template["Resources"]
module = Path(__file__).resolve().parents[1] / "terraform/qualification/execution"
account = "111122223333"
run = "sbq-123-1"
region = "us-east-2"
vpc = "vpc-00000000000000001"
private_zone = "Z12345ABCDE"
discovery_zone = "Z98765ZYXWV"
namespace = "ns-abcdefghijklmnop"
services = ["srv-abcdefghijklmnop", "srv-0123456789abcdef"]


def draft(**inputs):
    """Render the provider-free Terraform module using one stage's verified inputs."""
    args = {
        "testing_account_id": account,
        "publisher_account_id": "444455556666",
        "run_name": run,
        "host_ami_id": template["Mappings"]["ReleaseRegions"][region]["HostAmi"],
        "router_ami_id": "ami-00000000000000099",
        **inputs,
    }
    command = ["terraform", f"-chdir={module}", "console", "-no-color"]
    for key, value in args.items():
        value = json.dumps(value) if isinstance(value, list) else value
        command.extend(["-var", f"{key}={value}"])
    rendered = subprocess.check_output(
        command,
        input="jsonencode({ policies = local.policy_statements, boundaries = local.boundary_arns })\n",
        text=True,
    )
    return json.loads(json.loads(rendered))


def statements(rendered, action):
    """Select grants for an IAM action from the actual generated documents."""
    return [
        statement
        for policy in rendered["policies"].values()
        for statement in json.loads(policy)["Statement"]
        if action in statement["Action"]
    ]


def resolve(expression, phase, mode="external"):
    """Resolve the subset of intrinsics used for phase guards and IAM names."""
    params = {
        "Phase": phase, "RoleBoundaryMode": mode, "DeploymentName": run,
        "AccessMode": "tailscale", "AWS::Partition": "aws",
        "AWS::AccountId": account, "AWS::Region": region, "DnsSuffix": f"{run}.internal",
    }
    if not isinstance(expression, dict):
        return expression
    operation, value = next(iter(expression.items()))
    if operation == "Ref":
        return params[value]
    if operation == "Condition":
        return resolve(template["Conditions"][value], phase, mode)
    if operation == "If":
        condition = resolve({"Condition": value[0]}, phase, mode)
        return resolve(value[1] if condition else value[2], phase, mode)
    if operation == "Sub":
        for name, replacement in params.items():
            value = value.replace("${" + name + "}", replacement)
        assert "${" not in value, value
        return value
    values = [resolve(item, phase, mode) for item in value]
    if operation == "Equals":
        return values[0] == values[1]
    if operation == "Not":
        return not values[0]
    if operation == "And":
        return all(values)
    if operation == "Or":
        return any(values)
    raise AssertionError(f"Unexpected intrinsic {expression}")


def present(resource, phase, mode="external"):
    """Evaluate a resource's activation without contacting AWS."""
    condition = resource.get("Condition")
    return condition is None or resolve({"Condition": condition}, phase, mode)


# Role creation and inline updates are bound to the same exact role and boundary
# even though CloudFormation resolves the name and ARN independently.
foundation = draft()
roles = {name: spec for name, spec in resources.items() if spec["Type"] == "AWS::IAM::Role"}
assert roles.keys() == foundation["boundaries"].keys()
for name, spec in roles.items():
    properties = spec["Properties"]
    role_name = resolve(properties["RoleName"], "database")
    role_path = resolve(properties["Path"], "database")
    boundary = resolve(properties["PermissionsBoundary"], "database")
    role_arn = f"arn:aws:iam::{account}:role{role_path}{role_name}"
    assert boundary == foundation["boundaries"][name]
    for action in ["iam:CreateRole"] + (["iam:PutRolePolicy"] if properties.get("Policies") else []):
        grant, = [s for s in statements(foundation, action) if s["Resource"] == [role_arn]]
        assert grant["Condition"]["StringEquals"]["iam:PermissionsBoundary"] == boundary
    service = properties["AssumeRolePolicyDocument"]["Statement"][0]["Principal"]["Service"]
    grant, = [s for s in statements(foundation, "iam:PassRole") if role_arn in s["Resource"]]
    assert grant["Condition"]["StringEquals"]["iam:PassedToService"] == service

# Regional IAM names and a separate operator policy are allowed only in the
# self-managed console lane. The qualification role cannot create policies.
assert not statements(foundation, "iam:CreatePolicy")
assert not any(present(s, "database") for s in resources.values() if s["Type"] == "AWS::IAM::ManagedPolicy")
assert present(resources["SetupOperatorPolicy"], "database", "managed")
profile_grant, = statements(foundation, "iam:CreateInstanceProfile")
for name in ("HostProfile", "RouterProfile"):
    props = resources[name]["Properties"]
    path = resolve(props["Path"], "database")
    profile = resolve(props["InstanceProfileName"], "database")
    assert f"arn:aws:iam::{account}:instance-profile{path}{profile}" in profile_grant["Resource"]

# The four retained names are writable at creation, with value reads outside the execution role.
secret_grant = {arn for s in statements(foundation, "secretsmanager:CreateSecret") for arn in s["Resource"]}
for name in ("PostgresAdminSecret", "PostgresAppSecret", "PostgresDexSecret", "PostgresTlsSecret"):
    physical_name = resolve(resources[name]["Properties"]["Name"], "foundation")
    assert f"arn:aws:secretsmanager:{region}:{account}:secret:{physical_name}-??????" in secret_grant
assert not statements(foundation, "secretsmanager:GetSecretValue")

# DNS handoffs expose exact, successive IDs before the next class of write is reviewed.
zones = {"PrivateZone", "DiscoveryNamespace"}
discovery = {"PostgresDiscovery", "ControllerDiscovery"}
records = {"PrivateRecords"}
for phase, expected in (
    ("foundation", set()), ("dns-zones", zones), ("dns-services", zones | discovery),
    ("database", zones | discovery | records), ("active", zones | discovery | records),
):
    assert {name for name in zones | discovery | records if present(resources[name], phase)} == expected
    assert all(present(resources[name], phase) == (phase in {"database", "active"})
               for name in ("PostgresService", "ControllerService", "SetupSession"))

assert not statements(foundation, "route53:CreateHostedZone")
network = draft(verified_vpc_id=vpc)
zone_grant, = statements(network, "route53:CreateHostedZone")
assert zone_grant["Condition"]["StringEquals"]["route53:VPCs"] == f"VPCId={vpc},VPCRegion={region}"
assert not statements(network, "route53:ChangeResourceRecordSets")
assert not statements(network, "servicediscovery:TagResource")
verified = draft(
    verified_vpc_id=vpc, verified_private_zone_id=private_zone,
    verified_discovery_zone_id=discovery_zone, verified_namespace_id=namespace,
    verified_discovery_service_ids=services,
)
change_grant, = statements(verified, "route53:ChangeResourceRecordSets")
assert change_grant["Resource"] == [f"arn:aws:route53:::hostedzone/{private_zone}"]
record_keys = change_grant["Condition"]["ForAllValues:StringEquals"]
record_specs = resources["PrivateRecords"]["Properties"]["RecordSets"]
assert set(record_keys["route53:ChangeResourceRecordSetsNormalizedRecordNames"]) == {
    resolve(spec["Name"], "database").rstrip(".").replace("*", "\\052") for spec in record_specs
}
assert set(record_keys["route53:ChangeResourceRecordSetsRecordTypes"]) == {spec["Type"] for spec in record_specs}
for action in ("servicediscovery:TagResource", "servicediscovery:DeleteService"):
    assert all("*" not in arn for s in statements(verified, action) for arn in s["Resource"])

print("Packaged CloudFormation and execution policy contract passed.")
