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
import subprocess
import sys
from types import ModuleType
from unittest.mock import patch

import yaml

root, fixture_dir = map(Path, sys.argv[1:])
cloudformation = root / "platform/cloudformation/aws"
template_bytes = (cloudformation / "template.yaml").read_text()
template = yaml.safe_load(template_bytes)
resources = template["Resources"]
parameters = template["Parameters"]
outputs = template["Outputs"]


def properties(name):
    """Get the properties of a required stack resource."""
    return resources[name]["Properties"]


def get_discovery(attribute):
    """Reference a validated launch-discovery result."""
    return {"Fn::GetAtt": ["LaunchDiscovery", attribute]}


# The source is the deployable one-pass template rather than an input to a release-time renderer.
assert not re.search(r"@@[A-Z_]+@@", template_bytes), "Template needs a release-time renderer"
assert "Transform" not in template
assert not any(r["Type"] == "AWS::CloudFormation::Stack" for r in resources.values())
assert len(template_bytes.encode()) <= 1_048_576
assert template["Metadata"]["AWSToolsMetrics"]["AWSAgentToolkit"] == "aws-cloudformation@3"
assert set(parameters) == {
    "ServiceSubnet", "WorkspaceSubnet", "DnsSuffix", "OperatorCidr", "DataVolumeSizeGiB",
    "OciImageCacheSizeGiB", "PublicIngressCidr", "AcmeContactEmail", "AcmeTermsAccepted",
    "BrowserAccess", "PublicDnsZone", "DnsPlacement", "ReleasePins",
}
assert parameters["ReleasePins"]["Type"] == "String" and "Default" not in parameters["ReleasePins"]
assert properties("LaunchDiscovery")["ReleasePins"] == {"Ref": "ReleasePins"}

# A combination of wrong region, missing pin or unapproved image must fail before any pin reaches AWS resources.
pins = {
    "Region": "us-east-2",
    "ControllerImage": "ghcr.io/jflatten/shiftbox@sha256:" + "a" * 64,
    "PostgresImage": "docker.io/library/postgres:17-bookworm@sha256:" + "b" * 64,
    "HostAmi": "ami-00000000000000001",
    "WorkspaceAmi": "ami-00000000000000002",
    "PublisherAccount": "123456789012",
    "WorkspaceRootDevice": "/dev/sda1",
    "WorkspaceSnapshot": "snap-00000000000000001",
    "WorkspaceSourceAmi": "ami-00000000000000003",
    "WorkspaceSourceRegion": "us-east-1",
    "WorkspaceSourceRevision": "c" * 40,
    "WorkspaceRelease": "v1.0.0-rc.5",
    "GuestRelease": "v1.0.0-rc.5",
    "WorkspaceArchitecture": "amd64",
    "WorkspaceCatalogSHA256": "d" * 64,
    "WorkspaceMinimumDiskGiB": "32",
}
namespace = {}
source = properties("DiscoveryFunction")["Code"]["ZipFile"]
with patch.dict(sys.modules, {"cfnresponse": ModuleType("cfnresponse")}):
    exec(compile(source, "<CloudFormation discovery Lambda>", "exec"), namespace)
parse_pins = namespace["parse_release_pins"]
assert parse_pins(json.dumps(pins), "us-east-2") == pins
assert len(json.dumps(pins)) < parameters["ReleasePins"]["MaxLength"]
assert len(json.dumps(pins).encode()) + 1024 < 4096
for bad in (
    "{", "[]", json.dumps({k: v for k, v in pins.items() if k != "WorkspaceAmi"}),
    json.dumps(pins | {"Unexpected": "value"}),
    json.dumps(pins | {"Region": "us-west-2"}),
    json.dumps(pins | {"ControllerImage": "docker.io/another/controller:latest"}),
    json.dumps(pins | {"PostgresImage": "docker.io/library/postgres:17"}),
    json.dumps(pins | {"WorkspaceAmi": "*"}),
    json.dumps(pins | {"PublisherAccount": "another-account"}),
    json.dumps(pins | {"WorkspaceSourceRevision": "main"}),
    json.dumps(pins | {"WorkspaceMinimumDiskGiB": 32}),
    json.dumps(pins | {"GuestRelease": "v1.0.0-rc.4"}),
):
    try:
        parse_pins(bad, "us-east-2")
    except ValueError:
        continue
    raise AssertionError("Unexpectedly accepted invalid release inputs")

# The exact regional ECS host, controller, PostgreSQL and workspace provenance pins come from launch discovery.
assert properties("EcsHost")["ImageId"] == get_discovery("HostAmi")
controller = properties("ControllerTask")["ContainerDefinitions"][0]
postgres = properties("PostgresTask")["ContainerDefinitions"][0]
assert controller["Image"] == get_discovery("ControllerImage")
assert postgres["Image"] == get_discovery("PostgresImage")
runtime = next(entry["Value"] for entry in controller["Environment"] if entry["Name"] == "SHIFTBOX_CLOUD_CONFIG")
runtime_body, bindings = runtime["Fn::Sub"]
expected_runtime_pins = {
    "ImageId": "WorkspaceAmi", "ImageOwner": "PublisherAccount", "RootDevice": "WorkspaceRootDevice",
    "Snapshot": "WorkspaceSnapshot", "SourceAmi": "WorkspaceSourceAmi", "SourceRegion": "WorkspaceSourceRegion",
    "SourceRevision": "WorkspaceSourceRevision", "ImageRelease": "WorkspaceRelease", "GuestRelease": "GuestRelease",
    "Architecture": "WorkspaceArchitecture", "MinimumDisk": "WorkspaceMinimumDiskGiB",
    "CatalogSHA256": "WorkspaceCatalogSHA256",
}
assert all(bindings[name] == get_discovery(attribute) for name, attribute in expected_runtime_pins.items())
assert all(f'${{{name}}}' in runtime_body for name in expected_runtime_pins)
assert '"subnetId":"${WorkspaceSubnet}"' in runtime_body
assert '${InstallationVolume}' in runtime_body
assert all(field not in template_bytes for field in ("GitHubAppSecretArn", "HasGitHubApp", "SHIFTBOX_GITHUB_APP"))

# VPC, service availability zone and public subnet come from the tested discovery path.
assert parameters["ServiceSubnet"]["Type"] == parameters["WorkspaceSubnet"]["Type"] == "AWS::EC2::Subnet::Id"
assert parameters["PublicDnsZone"]["Type"] == "AWS::Route53::HostedZone::Id"
assert "SameVpc" in template["Rules"]
assert properties("DiscoveryNamespace")["Vpc"] == get_discovery("VpcId")
assert properties("PrivateZone")["VPCs"] == [{"VPCId": get_discovery("VpcId"), "VPCRegion": {"Ref": "AWS::Region"}}]
assert outputs["VpcId"]["Value"] == get_discovery("VpcId")
assert outputs["ServiceSubnetId"]["Value"] == {"Ref": "ServiceSubnet"}
assert outputs["WorkspaceSubnetId"]["Value"] == {"Ref": "WorkspaceSubnet"}
assert outputs["WorkspaceSecurityGroupId"]["Value"] == {"Ref": "WorkspaceGroup"}
assert not ({"PrivateRouteTableId", "TailscaleRouterInstanceId", "TailscaleAdvertisedRoute"} & outputs.keys())

# Selecting existing subnets keeps the stack out of shared networking and keeps hosts and services private.
forbidden_types = {
    "AWS::EC2::VPC", "AWS::EC2::Subnet", "AWS::EC2::RouteTable", "AWS::EC2::Route",
    "AWS::EC2::SubnetRouteTableAssociation", "AWS::EC2::InternetGateway", "AWS::EC2::VPCGatewayAttachment",
    "AWS::EC2::NatGateway",
}
assert not (forbidden_types & {r["Type"] for r in resources.values()})
assert {name for name, r in resources.items() if r["Type"] == "AWS::EC2::Instance"} == {"EcsHost"}
host_interface, = properties("EcsHost")["NetworkInterfaces"]
assert host_interface["SubnetId"] == {"Ref": "ServiceSubnet"} and host_interface["AssociatePublicIpAddress"] is False
for name in ("ControllerService", "PostgresService"):
    assert properties(name)["NetworkConfiguration"]["AwsvpcConfiguration"]["AssignPublicIp"] == "DISABLED"
for name, resource in resources.items():
    if resource["Type"] == "AWS::EC2::SecurityGroup" and name != "PublicIngressGroup":
        assert "SecurityGroupIngress" not in resource["Properties"], name

# Public access remains opt-in, limits ingress to explicit HTTPS, and can be turned off without replacing management DNS.
assert parameters["BrowserAccess"]["Default"] == "Private"
assert parameters["BrowserAccess"]["AllowedValues"] == ["Public", "Private"]
assert template["Conditions"]["PublicEnabled"] == {"Fn::Equals": [{"Ref": "BrowserAccess"}, "Public"]}
for name in ("PublicAddress", "PublicLoadBalancer", "PublicListener", "PublicTargets", "ControllerPublicIngress", "PublicBrowserRecords"):
    assert resources[name]["Condition"] == "PublicEnabled", name
assert properties("PublicLoadBalancer")["SubnetMappings"] == [
    {"SubnetId": get_discovery("PublicSubnet"), "AllocationId": {"Fn::GetAtt": ["PublicAddress", "AllocationId"]}},
]
assert properties("PublicIngressGroup")["SecurityGroupIngress"] == [
    {"IpProtocol": "tcp", "FromPort": 443, "ToPort": 443, "CidrIp": {"Ref": "PublicIngressCidr"}},
]
assert properties("PublicListener")["Protocol"] == "TCP" and properties("PublicListener")["Port"] == 443
assert properties("PublicTargets")["TargetType"] == "ip"
assert properties("ControllerPublicIngress")["SourceSecurityGroupId"] == {"Ref": "PublicIngressGroup"}
assert properties("OperatorIngress")["CidrIp"] == {"Ref": "OperatorCidr"}
assert re.fullmatch(parameters["OperatorCidr"]["AllowedPattern"], "0.0.0.0/0") is None
assert resources["OperatorIngress"]["Condition"] == "AllowOperatorIngress"
assert properties("GuestIngress")["SourceSecurityGroupId"] == {"Ref": "WorkspaceGroup"}
assert properties("ControllerService")["LoadBalancers"] == {"Fn::If": [
    "PublicEnabled",
    [{"ContainerName": "controller", "ContainerPort": 443, "TargetGroupArn": {"Ref": "PublicTargets"}}],
    [],
]}
assert resources["ManagedPublicZone"]["Condition"] == "CreateManagedPublicZone"
assert "Condition" not in resources["ManagedPrivateBrowserRecords"]
assert properties("ManagedPrivateBrowserRecords")["HostedZoneId"] == {"Fn::GetAtt": ["PrivateBrowserZone", "ZoneId"]}

# One stack create starts the TLS-backed databases and the pending-activation controller without a separate setup task.
assert controller["Command"] == ["one-pass"]
for name in ("DiscoveryNamespace", "PostgresDiscovery", "PrivateRecords", "PostgresService", "ControllerService", "SetupClaim"):
    assert "Condition" not in resources[name], name
assert properties("ControllerService")["DesiredCount"] == 1
assert "PrivateRecords" in resources["PostgresService"]["DependsOn"]
assert not ({"PostgresTlsSecret", "SetupTask", "SetupSession"} & resources.keys())
assert "database-trust" in str(properties("ControllerTask")["Volumes"])
assert "pg-identity" in str(properties("PostgresTask")["Volumes"])
assert outputs["SetupClaimSecretArn"]["Value"] == {"Ref": "SetupClaim"}
assert outputs["ManagementOrigin"]["Value"]["Fn::Sub"][0] == "https://${DeploymentName}.${BaseDomain}"

# Recreating a stack with the same name gives both private DNS targets a fresh Cloud Map name.
domain = properties("DiscoveryNamespace")["Name"]
assert domain == {"Fn::Sub": ["svc-${StackInstanceId}.${DnsSuffix}", {
    "StackInstanceId": {"Fn::Select": [2, {"Fn::Split": ["/", {"Ref": "AWS::StackId"}]}]},
    "DnsSuffix": get_discovery("DnsSuffix"),
}]}
for name in ("PrivateRecords", "ManagedPrivateBrowserRecords"):
    records = properties(name)["RecordSets"]
    targets = [
        v["Fn::Sub"] for record in records for v in record.get("ResourceRecords", [])
        if isinstance(v, dict) and "ServiceDiscoveryDomain" in v.get("Fn::Sub", ["", {}])[1]
    ]
    assert targets and all(mapping["ServiceDiscoveryDomain"] == domain for _, mapping in targets), name

# Stateful data and generated credentials survive deletion and replacement, while the separate cache is disposable.
protected = {"InstallationVolume", "PostgresAdminSecret", "PostgresAppSecret", "PostgresDexSecret"}
assert properties("InstallationVolume")["Encrypted"] is True
assert properties("InstallationVolume")["AvailabilityZone"] == get_discovery("AvailabilityZone")
for name in protected | {"SetupClaim"}:
    assert resources[name]["DeletionPolicy"] == resources[name]["UpdateReplacePolicy"] == "Retain", name
policy = json.loads((cloudformation / "stack-policy.json").read_text())
denial = next(statement for statement in policy["Statement"] if statement["Effect"] == "Deny")
assert set(denial["Action"]) == {"Update:Replace", "Update:Delete"}
assert protected <= {name.rsplit("/", 1)[-1] for name in denial["Resource"]}
assert all(field not in template_bytes for field in ("BEGIN PRIVATE KEY", "resolve:secretsmanager:"))
assert not any(re.search(r"password|privatekey", name, re.I) for name in parameters)
for name in protected - {"InstallationVolume"} | {"SetupClaim"}:
    assert properties(name)["GenerateSecretString"]["PasswordLength"] >= 32
    assert "SecretString" not in properties(name)
assert "SetupClaim" in repr(controller["Secrets"])
assert parameters["OciImageCacheSizeGiB"]["Default"] == 20
cache = resources["RegistryCacheVolume"]
assert cache["DeletionPolicy"] == cache["UpdateReplacePolicy"] == "Delete"
assert cache["Properties"]["Encrypted"] is True and cache["Properties"]["VolumeType"] == "gp3"
assert cache["Properties"]["AvailabilityZone"] == get_discovery("AvailabilityZone")
assert cache["Properties"]["Size"] == {"Ref": "OciImageCacheSizeGiB"}
assert "RegistryCacheAttachment" in resources["ControllerService"]["DependsOn"]
assert {"Name": "registry-cache", "Host": {"SourcePath": "/var/lib/shiftbox-registry-cache"}} in properties("ControllerTask")["Volumes"]
assert not any(volume.get("Name") == "registry-cache" for volume in properties("PostgresTask")["Volumes"])

# Runtime roles retain the reviewed permission ceilings and only the controller role can launch a workspace.
roles = {name for name, resource in resources.items() if resource["Type"] == "AWS::IAM::Role"}
assert roles == {"HostRole", "PostgresExecutionRole", "ControllerExecutionRole", "ControllerTaskRole", "DiscoveryRole"}
for name in roles:
    role = properties(name)
    assert {"RoleName", "Path", "PermissionsBoundary"} <= role.keys()
    boundary = properties(role["PermissionsBoundary"]["Ref"])["PolicyDocument"]
    if name == "HostRole":
        for statement in boundary["Statement"]:
            actions = statement["Action"] if isinstance(statement["Action"], list) else [statement["Action"]]
            assert all(action.startswith(("ec2:", "ecs:", "logs:")) and "*" not in action for action in actions)
    else:
        assert boundary == role["Policies"][0]["PolicyDocument"], name
    if name != "ControllerTaskRole":
        assert "ec2:RunInstances" not in str(boundary)
# Only the exact private test destination can pull the test package using Secrets Manager.
assert "RepositoryCredentials" not in postgres
assert controller["RepositoryCredentials"] == {"Fn::If": [
    "PrivateTestArtifacts", {"CredentialsParameter": "shiftbox-main-test-ghcr"}, {"Ref": "AWS::NoValue"}
]}
assert template["Conditions"]["PrivateTestArtifacts"] == {"Fn::And": [
    {"Fn::Equals": [{"Ref": "AWS::AccountId"}, "888293065132"]},
    {"Fn::Equals": [{"Ref": "AWS::Region"}, "us-east-2"]},
    {"Fn::Equals": [{"Ref": "AWS::StackName"}, "sbtest-main"]},
]}
for task in ("Controller", "Postgres"):
    execution_role = properties(f"{task}ExecutionRole")
    assert not execution_role.get("ManagedPolicyArns")
    logs = next(statement for statement in execution_role["Policies"][0]["PolicyDocument"]["Statement"]
                if isinstance(statement["Action"], list))
    assert set(logs["Action"]) == {"logs:CreateLogStream", "logs:PutLogEvents"}
    assert logs["Resource"]["Fn::Sub"].endswith(f":log-stream:ecs/{task.lower()}/*")
assert properties("ControllerTask")["ExecutionRoleArn"] != properties("PostgresTask")["ExecutionRoleArn"]
statements = properties("ControllerTaskRole")["Policies"][0]["PolicyDocument"]["Statement"]
by_sid = {statement["Sid"]: statement for statement in statements if "Sid" in statement}
image = by_sid["LaunchOfficialImage"]
assert image["Action"] == "ec2:RunInstances"
assert image["Resource"]["Fn::Sub"][0].endswith("::image/${ImageId}")
assert image["Resource"]["Fn::Sub"][1]["ImageId"] == get_discovery("WorkspaceAmi")
assert image["Condition"]["StringEquals"]["ec2:Owner"] == get_discovery("PublisherAccount")
assert by_sid["LaunchOwnedEncryptedVolumes"]["Condition"]["Bool"]["ec2:Encrypted"] == "true"
assert by_sid["LaunchOwnedPrivateInterfaces"]["Condition"]["BoolIfExists"]["ec2:AssociatePublicIpAddress"] == "false"
challenge = next(statement for statement in statements if statement["Action"] == "route53:ChangeResourceRecordSets")
conditions = challenge["Condition"]["ForAllValues:StringEquals"]
assert conditions["route53:ChangeResourceRecordSetsRecordTypes"] == ["TXT"]
assert conditions["route53:ChangeResourceRecordSetsActions"] == ["UPSERT", "DELETE"]
assert [entry["Fn::Sub"][0] for entry in conditions["route53:ChangeResourceRecordSetsNormalizedRecordNames"]] == [
    f"_acme-challenge.{prefix}${{DeploymentName}}.${{BaseDomain}}" for prefix in ("", "ide.", "preview.", "repair.")
]

# The actual checked-in host bootstrap fits EC2 limits and keeps reviewed quoting and cache startup order.
script, substitutions = properties("EcsHost")["UserData"]["Fn::Base64"]["Fn::Sub"]
assert len(script.encode()) + sum(len(value.encode()) for value in substitutions.values() if isinstance(value, str)) + 1024 <= 16_384
user_data_values = {
    "Cluster": "test-cluster",
    "DeploymentName": "test-deployment",
    "InstallationVolume": "vol-0123456789abcdef0",
    "InitializeDataVolume": "true",
    "RegistryCacheVolume": "vol-1234567890abcdef0",
    "MountScript": substitutions["MountScript"],
    "MountCacheScript": substitutions["MountCacheScript"],
    "InstallationTlsScript": substitutions["InstallationTlsScript"],
    "InstallationTlsUnit": "ExecStartPost=/usr/local/libexec/shiftbox-installation-tls /var/lib/shiftbox-data test.internal us-east-2 -",
}
rendered = re.sub(r"\$\{([A-Za-z0-9_]+)\}", lambda match: user_data_values[match[1]], script)
subprocess.run(["bash", "-n"], input=rendered, text=True, check=True)
assert "ExecStart=/usr/local/libexec/shiftbox-mount-registry-cache vol-1234567890abcdef0 /var/lib/shiftbox-registry-cache auto" in rendered
assert "Wants=shiftbox-cache.service\nAfter=shiftbox-cache.service" in rendered
subprocess.run(["sh", "-n"], input=postgres["Command"][0], text=True, check=True)
(fixture_dir / "installation-tls.sh").write_text(substitutions["InstallationTlsScript"])
print("CloudFormation structure tests passed.")

PY

# The host bootstrap issues a verified database identity in managed HTTPS
# mode and reuses it after a restart without publishing a browser CA.
mkdir "$fixture_dir/host-bin" "$fixture_dir/installation"
printf '#!/bin/sh\nexit 0\n' > "$fixture_dir/host-bin/chown"
printf '#!/bin/sh\nexit 99\n' > "$fixture_dir/host-bin/aws"
chmod +x "$fixture_dir/host-bin/chown" "$fixture_dir/host-bin/aws"
tls_bootstrap="$fixture_dir/installation-tls.sh"
installation="$fixture_dir/installation"
PATH="$fixture_dir/host-bin:$PATH" sh "$tls_bootstrap" "$installation" test.internal us-east-2 - 2>"$fixture_dir/openssl.log"
openssl verify -CAfile "$installation/database-trust/ca.crt" -purpose sslserver \
  -verify_hostname postgres.test.internal "$installation/pg-identity/server.crt" >/dev/null
test ! -e "$installation/browser-identity"
test ! -e "$installation/database-trust/server.key"
cp "$installation/pg-identity/server.crt" "$fixture_dir/first-server.crt"
PATH="$fixture_dir/host-bin:$PATH" sh "$tls_bootstrap" "$installation" test.internal us-east-2 - 2>>"$fixture_dir/openssl.log"
cmp "$fixture_dir/first-server.crt" "$installation/pg-identity/server.crt"

# An independent fixture can inspect the exact deployable template.
if [[ -n ${SHIFTBOX_TEST_TEMPLATE_OUTPUT:-} ]]; then
  cp -- "$root/platform/cloudformation/aws/template.yaml" "$SHIFTBOX_TEST_TEMPLATE_OUTPUT"
fi

# Opt in to the CloudFormation tooling layers with provisioned, pinned tools.
if [[ ${1:-} == --aws-tooling ]]; then
  cfn-lint --regions us-east-1 us-east-2 us-west-1 us-west-2 -- "$root/platform/cloudformation/aws/template.yaml"
  cfn-guard test --rules-file "$root/platform/cloudformation/aws/shiftbox.guard" \
    --test-data "$root/platform/cloudformation/aws/tests/guard/template-tests.yaml"
  cfn-guard validate --rules "$root/platform/cloudformation/aws/shiftbox.guard" \
    --data "$root/platform/cloudformation/aws/template.yaml"
fi
