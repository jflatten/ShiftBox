#!/usr/bin/env bash
# Verifies the stack security model without AWS access or publisher artifacts.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT

python3 - "$root" "$fixture_dir" <<'PY'
import json
from pathlib import Path
import re
import runpy
import subprocess
import sys

import yaml

root, fixture_dir = map(Path, sys.argv[1:])
cloudformation = root / "platform/cloudformation/aws"


class CloudFormationLoader(yaml.SafeLoader):
    """Keep CloudFormation short forms visible for local assertions."""


def read_intrinsic(loader, tag, node):
    """Parse static template values without evaluating their expressions."""
    if isinstance(node, yaml.SequenceNode):
        value = loader.construct_sequence(node, deep=True)
    elif isinstance(node, yaml.MappingNode):
        value = loader.construct_mapping(node, deep=True)
    else:
        value = loader.construct_scalar(node)
    return {tag: value}


CloudFormationLoader.add_multi_constructor("!", read_intrinsic)

# Approved release pins are substituted at build time. These values are all synthetic.
regions = ("us-east-1", "us-east-2", "us-west-1", "us-west-2")
pins = {
    region: {
        "HostAmi": f"ami-{number:017x}",
        "WorkspaceAmi": f"ami-{number + 4:017x}",
        "PublisherAccount": "123456789012",
        "WorkspaceRootDevice": "/dev/sda1",
        "WorkspaceSnapshot": f"snap-{number:017x}",
        "WorkspaceSourceAmi": "ami-00000000000000009",
        "WorkspaceSourceRegion": "us-east-1",
        "WorkspaceSourceRevision": "a" * 40,
        "WorkspaceRelease": "v1.0.0-rc.1",
        "GuestRelease": "v1.0.0-rc.1",
        "WorkspaceArtifact": "workspace-ami",
        "WorkspaceArchitecture": "amd64",
        "WorkspaceCatalogSHA256": "b" * 64,
        "WorkspaceMinimumDiskGiB": "32",
    }
    for number, region in enumerate(regions, start=1)
}
controller = "ghcr.io/jflatten/shiftbox@sha256:" + "c" * 64
postgres = "docker.io/library/postgres:17-bookworm@sha256:" + "d" * 64
substitutions = {
    "CONTROLLER_IMAGE": controller,
    "POSTGRES_IMAGE": postgres,
    "RELEASE_REGIONS": pins,
    "MOUNT_DATA_SCRIPT": (cloudformation / "host/mount-data.sh").read_text(),
    "MOUNT_CACHE_SCRIPT": (cloudformation / "host/mount-registry-cache.sh").read_text(),
    "POSTGRES_START_SCRIPT": (cloudformation / "postgres/start.sh").read_text(),
}
template_bytes = (cloudformation / "template.yaml").read_text()
# Exercise the production renderer, then independently compare the parsed
# boundary and inline policy documents below.
render_task_boundaries = runpy.run_path(str(cloudformation / "render_boundaries.py"))["render_task_boundaries"]
template_bytes = render_task_boundaries(template_bytes)
for key, value in substitutions.items():
    token = f"@@{key}@@"
    assert token in template_bytes, f"Missing release pin or script: {key}"
    template_bytes = template_bytes.replace(token, json.dumps(value))
assert not re.search(r"@@[A-Z_]+@@", template_bytes), "Unrendered template asset"
template = yaml.load(template_bytes, Loader=CloudFormationLoader)
resources = template["Resources"]

# The input sizes a separate, disposable disk that only the controller uses.
cache_parameter = template["Parameters"]["OciImageCacheSizeGiB"]
assert cache_parameter["Type"] == "Number" and cache_parameter["Default"] == 20
assert "up to 5 active users" in cache_parameter["Description"]
assert "6 to 20" in cache_parameter["Description"] and "21 to 50" in cache_parameter["Description"]
assert template["Metadata"]["AWS::CloudFormation::Interface"]["ParameterLabels"]["OciImageCacheSizeGiB"]["default"] == "OCI image cache size (GiB)"
cache_volume = resources["RegistryCacheVolume"]
assert cache_volume["Type"] == "AWS::EC2::Volume"
assert cache_volume["DeletionPolicy"] == cache_volume["UpdateReplacePolicy"] == "Delete"
cache_properties = cache_volume["Properties"]
assert cache_properties["AvailabilityZone"] == {"Ref": "AvailabilityZone"}
assert cache_properties["Size"] == {"Ref": "OciImageCacheSizeGiB"}
assert cache_properties["Encrypted"] is True and cache_properties["VolumeType"] == "gp3"
assert {"Key": "shiftbox-volume-purpose", "Value": "oci-image-cache"} in cache_properties["Tags"]
assert resources["RegistryCacheAttachment"]["Properties"] == {
    "Device": "/dev/sdg", "InstanceId": {"Ref": "EcsHost"}, "VolumeId": {"Ref": "RegistryCacheVolume"},
}
assert "RegistryCacheAttachment" in resources["ControllerService"]["DependsOn"]
cache_path = "/var/lib/shiftbox-registry-cache"
controller_task = resources["ControllerTask"]["Properties"]
assert {"Name": "registry-cache", "Host": {"SourcePath": cache_path}} in controller_task["Volumes"]
controller_container = controller_task["ContainerDefinitions"][0]
assert {"SourceVolume": "registry-cache", "ContainerPath": cache_path} in controller_container["MountPoints"]
assert {"Name": "SHIFTBOX_IMAGE_CACHE_VOLUME_ID", "Value": {"Ref": "RegistryCacheVolume"}} in controller_container["Environment"]
for name in ("PostgresTask", "SetupTask"):
    assert all(volume["Name"] != "registry-cache" for volume in resources[name]["Properties"]["Volumes"])

# Existing subnet selection must not give the stack ownership of shared networking.
forbidden_network_types = {
    "AWS::EC2::VPC", "AWS::EC2::Subnet", "AWS::EC2::RouteTable", "AWS::EC2::Route",
    "AWS::EC2::SubnetRouteTableAssociation", "AWS::EC2::InternetGateway",
    "AWS::EC2::VPCGatewayAttachment", "AWS::EC2::NatGateway", "AWS::EC2::EIP",
}
assert not (forbidden_network_types & {r["Type"] for r in resources.values()})
assert not ({"AccessMode", "VpcCidr", "CreateNatGateway"} & template["Parameters"].keys())
# GitHub setup is separate from the stack and its runtime IAM permissions.
assert all(field not in template_bytes for field in ("GitHubAppSecretArn", "HasGitHubApp", "SHIFTBOX_GITHUB_APP"))
# Tenant stack tags such as Project must never collide with resource-level tags.
for resource in resources.values():
    properties = resource.get("Properties", {})
    for tag in properties.get("Tags", []) + properties.get("HostedZoneTags", []):
        assert tag["Key"].startswith("shiftbox-"), tag["Key"]
assert {name for name, r in resources.items() if r["Type"] == "AWS::EC2::Instance"} == {"EcsHost"}


def properties(name):
    """Get the properties of a required stack resource."""
    return resources[name]["Properties"]


# The console and CLI create only the foundation by default.
assert template["Parameters"]["Phase"]["Default"] == "foundation"
phases = ("foundation", "dns-zones", "dns-services", "database", "active")
assert tuple(template["Parameters"]["Phase"]["AllowedValues"]) == phases
assert resources["PostgresService"]["Condition"] == "DatabaseEnabled"
assert resources["ControllerService"]["Condition"] == "DatabaseEnabled"
assert properties("ControllerService")["DesiredCount"] == {"If": ["ControllerEnabled", 1, 0]}
assert resources["SetupTask"]["Condition"] == "DatabaseEnabled"
assert template["Conditions"]["ControllerEnabled"] == {"Equals": [{"Ref": "Phase"}, "active"]}


def phase_condition(expression, phase):
    """Evaluate only the intrinsics that control DNS and database staging."""
    if not isinstance(expression, dict):
        return expression
    (function, args), = expression.items()
    if function == "Condition":
        return phase_condition(template["Conditions"][args], phase)
    if function == "Ref":
        assert args == "Phase"
        return phase
    values = [phase_condition(arg, phase) for arg in args]
    if function == "Equals":
        return values[0] == values[1]
    if function == "Not":
        return not values[0]
    if function == "Or":
        return any(values)
    raise AssertionError(f"Unexpected phase condition: {function}")


# Verify physical DNS IDs can be reviewed before creating their dependents.
for index, phase in enumerate(phases):
    for name, first_phase in {
        "PrivateZone": 1, "DiscoveryNamespace": 1, "PostgresDiscovery": 2,
        "ControllerDiscovery": 2, "PrivateRecords": 3, "PostgresTask": 3,
        "PostgresExecutionRole": 3, "SetupExecutionRole": 3, "ControllerExecutionRole": 3,
        "SetupTaskRole": 3, "ControllerTaskRole": 3,
    }.items():
        assert phase_condition({"Condition": resources[name]["Condition"]}, phase) == (index >= first_phase), name
    for name, reference, first_phase in (
        ("PrivateZoneId", {"Ref": "PrivateZone"}, 1),
        ("DiscoveryNamespaceId", {"Ref": "DiscoveryNamespace"}, 1),
        ("DiscoveryHostedZoneId", {"GetAtt": "DiscoveryNamespace.HostedZoneId"}, 1),
        ("PostgresDiscoveryServiceId", {"GetAtt": "PostgresDiscovery.Id"}, 2),
        ("ControllerDiscoveryServiceId", {"GetAtt": "ControllerDiscovery.Id"}, 2),
    ):
        output = template["Outputs"][name]
        assert output["Value"] == reference
        assert phase_condition({"Condition": output["Condition"]}, phase) == (index >= first_phase), name
    trust_rule = template["Rules"]["DatabaseTrustRequired"]
    assert phase_condition(trust_rule["RuleCondition"], phase) == (index >= 3)
assert "PrivateRecords" in resources["PostgresService"]["DependsOn"]
assert template["Outputs"]["WorkspaceSecurityGroupId"]["Value"] == {"Ref": "WorkspaceGroup"}

# The qualification role can only administer its exact named resources and
# boundary ARNs. Public installs need distinct names across AWS regions.
assert template["Parameters"]["RoleBoundaryMode"]["Default"] == "managed"
assert template["Conditions"]["UseExternalBoundaries"] == {"Equals": [{"Ref": "RoleBoundaryMode"}, "external"]}
assert template["Conditions"]["UseManagedBoundaries"] == {"Not": [{"Condition": "UseExternalBoundaries"}]}
roles = {name for name, resource in resources.items() if resource["Type"] == "AWS::IAM::Role"}
assert roles == {
    "HostRole", "PostgresExecutionRole", "SetupExecutionRole",
    "ControllerExecutionRole", "SetupTaskRole", "ControllerTaskRole",
}
managed_role_names = set()
for name in roles | {"HostProfile"}:
    identity = properties(name)
    name_property = "RoleName" if name in roles else "InstanceProfileName"
    condition, external_name, managed_name = identity[name_property]["If"]
    assert condition == "UseExternalBoundaries"
    assert external_name == {"Sub": "${DeploymentName}-" + name}
    public_name = managed_name["Sub"]
    assert "${AWS::Region}" in public_name
    assert len(public_name.replace("${DeploymentName}", "x" * 32).replace("${AWS::Region}", "us-east-2")) <= 64
    assert identity["Path"]["If"][:2] == ["UseExternalBoundaries", {"Sub": "/shiftbox-qualification/${DeploymentName}/"}]
    if name in roles:
        assert public_name not in managed_role_names
        managed_role_names.add(public_name)
        assert identity["PermissionsBoundary"]["If"] == [
            "UseExternalBoundaries",
            {"Sub": "arn:${AWS::Partition}:iam::${AWS::AccountId}:policy/shiftbox-qualification-boundaries/${DeploymentName}-" + name},
            {"Ref": name + "Boundary"},
        ]
        assert resources[name + "Boundary"]["Type"] == "AWS::IAM::ManagedPolicy"
        assert resources[name + "Boundary"]["Condition"] == (
            {
                "HostRole": "UseManagedBoundaries",
            }.get(name, "UseManagedTaskBoundaries")
        )
        if "Policies" in identity:
            assert properties(name + "Boundary")["PolicyDocument"] == identity["Policies"][0]["PolicyDocument"], name
assert resources["SetupOperatorPolicy"]["Condition"] == "ManageSetupOperatorPolicy"
assert template["Outputs"]["SetupOperatorPolicyArn"]["Condition"] == "ManageSetupOperatorPolicy"
assert template["Conditions"]["ManageSetupOperatorPolicy"] == {
    "And": [{"Condition": "UseManagedBoundaries"}, {"Condition": "DatabaseEnabled"}]
}
assert template["Conditions"]["UseManagedTaskBoundaries"] == {
    "And": [{"Condition": "UseManagedBoundaries"}, {"Condition": "DatabaseEnabled"}]
}

# Cap the managed ECS agent policy at the permissions used by this stack.
for name, allowed_prefixes in (
    ("HostRoleBoundary", ("ec2:", "ecs:", "logs:")),
):
    actions = {
        action
        for statement in properties(name)["PolicyDocument"]["Statement"]
        for action in (
            [statement["Action"]] if isinstance(statement["Action"], str) else statement["Action"]
        )
    }
    assert all(action.startswith(allowed_prefixes) and "*" not in action for action in actions), actions
    assert not ({"ecs:RunTask", "ssm:StartSession", "ssm:GetParameter", "ssm:GetParameters"} & actions)

# Review uses the same hosted image pins for both ECS paths.
assert properties("ControllerTask")["ContainerDefinitions"][0]["Image"] == controller
assert properties("SetupTask")["ContainerDefinitions"][0]["Image"] == controller
assert properties("PostgresTask")["ContainerDefinitions"][0]["Image"] == postgres
assert template["Mappings"]["ReleaseRegions"] == pins
cloud_environments = [
    next(item["Value"] for item in properties(name)["ContainerDefinitions"][0]["Environment"]
         if item["Name"] == "SHIFTBOX_CLOUD_CONFIG")
    for name in ("SetupTask", "ControllerTask")
]
assert cloud_environments[0] == cloud_environments[1], "Setup and controller provenance drifted"

# Existing PostgreSQL data, installation state and pinned secret resources survive stack changes.
protected = {"InstallationVolume", "PostgresAdminSecret", "PostgresAppSecret", "PostgresDexSecret", "PostgresTlsSecret"}
assert properties("InstallationVolume")["Encrypted"] is True
assert properties("InstallationVolume")["AvailabilityZone"] == {"Ref": "AvailabilityZone"}
for name in protected:
    assert "Condition" not in resources[name], name
    assert resources[name]["DeletionPolicy"] == resources[name]["UpdateReplacePolicy"] == "Retain", name
for name in protected - {"InstallationVolume"}:
    assert properties(name)["Name"]["If"] == [
        "UseExternalBoundaries", {"Sub": "${DeploymentName}-" + name}, {"Ref": "AWS::NoValue"},
    ], name
policy = json.loads((cloudformation / "stack-policy.json").read_text())
denial = next(s for s in policy["Statement"] if s["Effect"] == "Deny")
assert set(denial["Action"]) == {"Update:Replace", "Update:Delete"}
assert protected <= {name.rsplit("/", 1)[-1] for name in denial["Resource"]}

# An operator-supplied certificate or password must stay in Secrets Manager, never the stack template.
assert "BEGIN PRIVATE KEY" not in template_bytes
assert "resolve:secretsmanager:" not in template_bytes
assert not any(re.search(r"password|privatekey", name, re.I) for name in template["Parameters"])
for name in ("PostgresAdminSecret", "PostgresAppSecret", "PostgresDexSecret"):
    assert properties(name)["GenerateSecretString"]["PasswordLength"] >= 32
assert "SecretString" not in properties("PostgresTlsSecret")
assert "GenerateSecretString" not in properties("PostgresTlsSecret")
setups = {item["Name"] for item in properties("SetupTask")["ContainerDefinitions"][0]["Secrets"]}
assert setups == {"SHIFTBOX_APP_PASSWORD", "SHIFTBOX_DEX_PASSWORD"}
assert properties("SetupTask")["ExecutionRoleArn"] != properties("PostgresTask")["ExecutionRoleArn"]
assert properties("SetupTask")["ExecutionRoleArn"] != properties("ControllerTask")["ExecutionRoleArn"]

# Anonymous image pulls need no registry credentials or ECR authorization on task roles.
for task, stream in (("Postgres", "postgres"), ("Setup", "setup"), ("Controller", "controller")):
    assert "RepositoryCredentials" not in properties(f"{task}Task")["ContainerDefinitions"][0]
    execution_role = properties(f"{task}ExecutionRole")
    assert not execution_role.get("ManagedPolicyArns")
    policy_statements = execution_role["Policies"][0]["PolicyDocument"]["Statement"]
    logs = next(statement for statement in policy_statements if isinstance(statement["Action"], list))
    assert set(logs["Action"]) == {"logs:CreateLogStream", "logs:PutLogEvents"}
    assert logs["Resource"]["Sub"].endswith(f":log-stream:ecs/{stream}/*")

# A one-line CA works in the console and is restored to PEM for both tasks.
expected_pem_input = "-----BEGIN CERTIFICATE-----|AAAA|-----END CERTIFICATE-----|"
assert re.fullmatch(template["Parameters"]["PostgresCaPem"]["AllowedPattern"], expected_pem_input)
for name in ("SetupTask", "ControllerTask"):
    ca_value = next(
        item["Value"] for item in properties(name)["ContainerDefinitions"][0]["Environment"]
        if item["Name"] == "SHIFTBOX_DB_CA"
    )
    assert ca_value == {"Join": ["\n", {"Split": ["|", {"Ref": "PostgresCaPem"}]}]}

# Native AWS selectors reference existing inputs and reject a mismatched VPC or host AZ.
parameters = template["Parameters"]
assert parameters["VpcId"]["Type"] == "AWS::EC2::VPC::Id"
assert parameters["ServiceSubnet"]["Type"] == parameters["WorkspaceSubnet"]["Type"] == "AWS::EC2::Subnet::Id"
assert [
    rule["Assert"]["EachMemberEquals"] for rule in template["Rules"]["SubnetsInSelectedVpc"]["Assertions"]
] == [
    [[{"ValueOf": ["ServiceSubnet", "VpcId"]}, {"ValueOf": ["WorkspaceSubnet", "VpcId"]}], {"Ref": "VpcId"}],
    [[{"ValueOf": ["ServiceSubnet", "AvailabilityZone"]}], {"Ref": "AvailabilityZone"}],
]
host_interface, = properties("EcsHost")["NetworkInterfaces"]
assert host_interface["SubnetId"] == {"Ref": "ServiceSubnet"}
assert host_interface["AssociatePublicIpAddress"] is False
assert properties("DiscoveryNamespace")["Vpc"] == {"Ref": "VpcId"}
assert properties("PrivateZone")["VPCs"] == [{"VPCId": {"Ref": "VpcId"}, "VPCRegion": {"Ref": "AWS::Region"}}]
assert '"subnetId":"${WorkspaceSubnet}"' in cloud_environments[0]["Sub"][0]
for name, parameter in (("VpcId", "VpcId"), ("ServiceSubnetId", "ServiceSubnet"), ("WorkspaceSubnetId", "WorkspaceSubnet")):
    assert template["Outputs"][name]["Value"] == {"Ref": parameter}
assert not ({"PrivateRouteTableId", "TailscaleRouterInstanceId", "TailscaleAdvertisedRoute"} & template["Outputs"].keys())
for name in ("ControllerService", "PostgresService"):
    assert properties(name)["NetworkConfiguration"]["AwsvpcConfiguration"]["AssignPublicIp"] == "DISABLED"
for name, resource in resources.items():
    if resource["Type"] == "AWS::EC2::SecurityGroup":
        assert "SecurityGroupIngress" not in resource["Properties"], name
ingress = {
    name: (resource["Properties"]["FromPort"], resource["Properties"]["ToPort"])
    for name, resource in resources.items()
    if resource["Type"] == "AWS::EC2::SecurityGroupIngress"
}
assert ingress == {
    "OperatorIngress": (8443, 8443),
    "GuestIngress": (19443, 19447),
    "PostgresControllerIngress": (5432, 5432),
    "PostgresSetupIngress": (5432, 5432),
}, ingress
for name in ingress:
    assert "0.0.0.0/0" != properties(name).get("CidrIp"), name
assert properties("OperatorIngress")["CidrIp"] == {"Ref": "OperatorCidr"}
assert re.fullmatch(template["Parameters"]["OperatorCidr"]["AllowedPattern"], "0.0.0.0/0") is None
assert parameters["OperatorCidr"]["Default"] == ""
assert resources["OperatorIngress"]["Condition"] == "AllowOperatorIngress"
assert template["Conditions"]["AllowOperatorIngress"] == {"Not": [{"Equals": [{"Ref": "OperatorCidr"}, ""]}]}
assert properties("GuestIngress")["SourceSecurityGroupId"] == {"Ref": "WorkspaceGroup"}

# Workspace IAM requires the exact official image and publisher, workspace scopes and encrypted EBS.
statements = properties("ControllerTaskRole")["Policies"][0]["PolicyDocument"]["Statement"]
by_sid = {statement["Sid"]: statement for statement in statements}
image = by_sid["LaunchOfficialImage"]
assert image["Action"] == "ec2:RunInstances"
assert image["Resource"]["Sub"][0].endswith("::image/${ImageId}")
assert image["Resource"]["Sub"][1]["ImageId"] == {"FindInMap": ["ReleaseRegions", {"Ref": "AWS::Region"}, "WorkspaceAmi"]}
assert image["Condition"]["StringEquals"]["ec2:Owner"] == {"FindInMap": ["ReleaseRegions", {"Ref": "AWS::Region"}, "PublisherAccount"]}
assert by_sid["LaunchOwnedEncryptedVolumes"]["Condition"]["Bool"]["ec2:Encrypted"] == "true"
assert by_sid["LaunchOwnedPrivateInterfaces"]["Condition"]["BoolIfExists"]["ec2:AssociatePublicIpAddress"] == "false"

# The setup outputs and SSM document identify a local, scoped and authenticated handoff.
assert template["Outputs"]["SetupConfig"]["Condition"] == "DatabaseEnabled"
setup_output = template["Outputs"]["SetupConfig"]["Value"]["Sub"]
assert set(json.loads(setup_output[0])) == {
    "account", "stack_id", "region", "cluster", "task_definition", "subnet",
    "security_group", "document", "port", "log_group", "tag",
}
assert properties("SetupSession")["DocumentType"] == "Session"
assert properties("SetupSession")["Content"]["properties"] == {
    "portNumber": "8444", "localPortNumber": "8444", "type": "LocalPortForwarding",
}

# Rendered user data and template must fit AWS limits before being hosted on S3.
assert len(template_bytes.encode()) <= 1_048_576
script, substitutions = properties("EcsHost")["UserData"]["Fn::Base64"]["Sub"]
assert len(script.encode()) + sum(len(value.encode()) for value in substitutions.values()) <= 16_384
# Hosted bootstrap runs from the rendered bytes. Detect broken shell quoting and heredocs.
user_data_values = {
    "Cluster": "test-cluster",
    "DeploymentName": "test-deployment",
    "InstallationVolume": "vol-0123456789abcdef0",
    "InitializeDataVolume": "true",
    "RegistryCacheVolume": "vol-1234567890abcdef0",
    "MountScript": substitutions["MountScript"],
    "MountCacheScript": substitutions["MountCacheScript"],
}
rendered = re.sub(r"\$\{([A-Za-z0-9_]+)\}", lambda match: user_data_values[match[1]], script)
subprocess.run(["bash", "-n"], input=rendered, text=True, check=True)
# Cache mounting waits before ECS starts and always targets the distinct EBS ID.
assert "ExecStart=/usr/local/libexec/shiftbox-mount-registry-cache vol-1234567890abcdef0 /var/lib/shiftbox-registry-cache auto" in rendered
assert "Wants=shiftbox-cache.service\nAfter=shiftbox-cache.service" in rendered
postgres_start = properties("PostgresTask")["ContainerDefinitions"][0]["Command"][0]
subprocess.run(["sh", "-n"], input=postgres_start, text=True, check=True)
(fixture_dir / "rendered.yaml").write_text(template_bytes)
print("CloudFormation structure tests passed.")
PY

# The independent execution-policy fixture can validate the same rendered
# template without exposing or depending on the test's temporary directory.
if [[ -n ${SHIFTBOX_TEST_TEMPLATE_OUTPUT:-} ]]; then
  cp -- "$fixture_dir/rendered.yaml" "$SHIFTBOX_TEST_TEMPLATE_OUTPUT"
fi

# Opt in to the CloudFormation tooling layers with provisioned, pinned tools.
if [[ ${1:-} == --aws-tooling ]]; then
  cfn-lint --regions us-east-1 us-east-2 us-west-1 us-west-2 -- "$fixture_dir/rendered.yaml"
  cfn-guard test --rules-file "$root/platform/cloudformation/aws/shiftbox.guard" \
    --test-data "$root/platform/cloudformation/aws/tests/guard/template-tests.yaml"
  cfn-guard validate --rules "$root/platform/cloudformation/aws/shiftbox.guard" \
    --data "$fixture_dir/rendered.yaml"
fi
