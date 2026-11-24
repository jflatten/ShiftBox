#!/usr/bin/env python3
"""Bind a rendered ShiftBox template to private Ohio development artifacts."""

import argparse
import base64
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import uuid

import yaml

from one_pass import configure_one_pass


ACCOUNT = "888293065132"
REGION = "us-east-2"
REPOSITORY = "shiftbox-development"
REGISTRY = f"{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com/{REPOSITORY}"
DIGEST = r"sha256:[0-9a-f]{64}"
AMI_ID = r"ami-(?:[0-9a-f]{8}|[0-9a-f]{17})"


class CloudFormationLoader(yaml.SafeLoader):
    """Preserve CloudFormation expressions as their JSON long forms."""


def intrinsic(loader, tag, node):
    """Convert a supported YAML tag while leaving regular scalar types intact."""
    if isinstance(node, yaml.SequenceNode):
        value = loader.construct_sequence(node, deep=True)
    elif isinstance(node, yaml.MappingNode):
        value = loader.construct_mapping(node, deep=True)
    else:
        value = loader.construct_scalar(node)
    if tag == "GetAtt" and isinstance(value, str):
        value = value.split(".", 1)
    key = tag if tag in ("Ref", "Condition") else f"Fn::{tag}"
    return {key: value}


CloudFormationLoader.add_multi_constructor("!", intrinsic)


def render_development(base, published_controller, controller=None, workspace=None, *, one_pass=False, starter_catalog=b""):
    """Keep all public pins except explicit, separately verified development overrides."""
    if not re.fullmatch(r"ghcr\.io/jflatten/shiftbox@" + DIGEST, published_controller):
        raise ValueError("expected the published digest-pinned ShiftBox baseline")
    if controller and not re.fullmatch(re.escape(REGISTRY) + "@" + DIGEST, controller):
        raise ValueError("controller must be an exact testing ECR digest")
    if one_pass and not controller:
        raise ValueError("one-pass setup requires a private development controller digest")
    if workspace and not re.fullmatch(AMI_ID, workspace):
        raise ValueError("workspace must be an exact Ohio AMI ID")

    template = deepcopy(base)
    template["Description"] = "Private ShiftBox development deployment restricted to the Ohio testing account."
    if one_pass:
        configure_one_pass(template)
    regions = template["Mappings"]["ReleaseRegions"]
    template["Mappings"]["ReleaseRegions"] = {REGION: regions[REGION]}
    image_settings = {}
    if workspace:
        image_settings["verifiedSingleBootAmi"] = workspace
    if one_pass:
        if not 0 < len(starter_catalog) <= 8192 or hashlib.sha256(starter_catalog).hexdigest() != regions[REGION]["WorkspaceCatalogSHA256"]:
            raise ValueError("starter catalog bytes must match the official regional digest")
        image_settings["starterCatalog"] = base64.b64encode(starter_catalog).decode("ascii")
    guarded_parameters = {"AWS::AccountId": ACCOUNT, "AWS::Region": REGION}
    if "RoleBoundaryMode" in template.get("Parameters", {}):
        guarded_parameters["RoleBoundaryMode"] = "managed"
    template.setdefault("Rules", {})["DevelopmentDestination"] = {"Assertions": [
        {"Assert": {"Fn::Equals": [{"Ref": name}, expected]},
         "AssertDescription": f"Development requires {name} to be {expected}."}
        for name, expected in guarded_parameters.items()
    ]}

    resources = template["Resources"]
    containers = [(resource["Properties"], container) for resource in resources.values()
                  if resource["Type"] == "AWS::ECS::TaskDefinition"
                  for container in resource["Properties"]["ContainerDefinitions"] if container["Image"] == published_controller]
    if not containers:
        raise ValueError("the rendered template does not contain the published controller pin")
    for _, container in containers:
        if controller:
            container["Image"] = controller
        if image_settings:
            config = next(item["Value"] for item in container["Environment"] if item["Name"] == "SHIFTBOX_CLOUD_CONFIG")
            substitution = config["Fn::Sub"]
            contents = substitution[0] if isinstance(substitution, list) else substitution
            if contents.count('"ec2":{') != 1:
                raise ValueError("the rendered controller config has no unique workspace provider binding")
            fields = json.dumps(image_settings, separators=(",", ":"))[1:-1]
            contents = contents.replace('"ec2":{', f'"ec2":{{{fields},', 1)
            config["Fn::Sub"] = [contents, substitution[1]] if isinstance(substitution, list) else contents

    if controller:
        grants = [
            {"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*",
             "Condition": {"StringEquals": {"aws:RequestedRegion": REGION}}},
            {"Effect": "Allow", "Action": ["ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
             "Resource": f"arn:aws:ecr:{REGION}:{ACCOUNT}:repository/{REPOSITORY}"},
        ]
        # The ECS agent uses the task definition's execution role for a private
        # image pull. Its identity policy and permissions boundary must agree.
        execution_roles = dict.fromkeys(task["ExecutionRoleArn"]["Fn::GetAtt"][0] for task, _ in containers)
        for name in execution_roles:
            resources[name]["Properties"]["Policies"].append({
                "PolicyName": "PullDevelopmentController",
                "PolicyDocument": {"Version": "2012-10-17", "Statement": deepcopy(grants)},
            })
            boundary = resources[name + "Boundary"]["Properties"]["PolicyDocument"]
            boundary["Statement"].extend(deepcopy(grants))
    if workspace:
        grant = {"Sid": "LaunchDevelopmentImage", "Effect": "Allow", "Action": "ec2:RunInstances",
                 "Resource": f"arn:aws:ec2:{REGION}::image/{workspace}",
                 "Condition": {"StringEquals": {"ec2:Owner": ACCOUNT}}}
        policy = next(policy for policy in resources["ControllerTaskRole"]["Properties"]["Policies"]
                      if policy["PolicyName"] == "OwnedWorkspaces")
        policy["PolicyDocument"]["Statement"].append(grant)
        resources["ControllerTaskRoleBoundary"]["Properties"]["PolicyDocument"]["Statement"].append(deepcopy(grant))
    return template


def verify_workspace_image(ami, build, response):
    """Verify that the pinned Packer artifact is private, encrypted and testing-owned."""
    images = response["Images"]
    expected = {"ImageId": ami, "OwnerId": ACCOUNT, "Public": False, "State": "available", "Architecture": "x86_64",
                "RootDeviceName": "/dev/sda1", "RootDeviceType": "ebs", "VirtualizationType": "hvm"}
    if len(images) != 1 or any(images[0].get(key) != value for key, value in expected.items()):
        raise ValueError("workspace AMI must be private, available and owned by the testing account")
    image = images[0]
    disks = [mapping for mapping in image["BlockDeviceMappings"] if "Ebs" in mapping]
    if (len(disks) != 1 or disks[0]["DeviceName"] != "/dev/sda1" or disks[0]["Ebs"].get("Encrypted") is not True
            or disks[0]["Ebs"].get("VolumeSize") != 32 or not disks[0]["Ebs"].get("SnapshotId")):
        raise ValueError("workspace AMI must have one encrypted 32 GiB root snapshot")
    tags = {tag["Key"]: tag["Value"] for tag in image["Tags"]}
    if any(tags.get(key) != value for key, value in {
        "ShiftBoxArtifact": "workspace-ami", "ShiftBoxRelease": build["releaseId"],
        "ShiftBoxSourceAMI": build["sourceAmiId"], "ShiftBoxSourceCommit": build["sourceRevision"],
    }.items()):
        raise ValueError("workspace AMI tags do not match the local Packer manifest")


def aws(*args):
    """Keep any live inspection on the named Ohio testing SSO profile."""
    return json.loads(subprocess.check_output(
        ["aws", "--profile", "shiftbox-testing", "--region", REGION, "--output", "json", *args],
        env=os.environ | {"AWS_PAGER": "", "AWS_CLI_AUTO_PROMPT": "off", "AWS_IGNORE_CONFIGURED_ENDPOINT_URLS": "true"},
        text=True,
    ))


def workspace_pin(catalog, compatible_guest):
    """Read the one local private Packer build and verify its live EC2 identity."""
    build, = catalog["builds"]
    metadata = build["custom_data"]
    region, ami = build["artifact_id"].split(":", 1)
    if (region != REGION or not re.fullmatch(AMI_ID, ami) or build["builder_type"] != "amazon-ebs"
            or uuid.UUID(build["packer_run_uuid"]) != uuid.UUID(catalog["last_run_uuid"])
            or metadata.get("publicBuild") != "false" or metadata["guestAgentRelease"] != compatible_guest
            or not re.fullmatch(r"dev-[a-z0-9-]{1,91}", metadata["releaseId"])):
        raise ValueError("use one private Ohio development Packer build with the matching guest compatibility ID")
    verify_workspace_image(ami, metadata, aws("ec2", "describe-images", "--owners", ACCOUNT, "--image-ids", ami))
    return ami


def main():
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument("template", type=Path)
    args.add_argument("baseline_manifest", type=Path)
    args.add_argument("output", type=Path)
    args.add_argument("regional_catalog", type=Path)
    args.add_argument("--controller", help="Exact private Ohio testing ECR image URI ending in @sha256:…")
    args.add_argument("--workspace", type=Path, help="Local private Ohio Packer manifest.json")
    args.add_argument("--one-pass", action="store_true", help="Exercise private UI setup with a development controller")
    parsed = args.parse_args()
    try:
        base = yaml.load(parsed.template.read_text(), Loader=CloudFormationLoader)
        manifest = json.loads(parsed.baseline_manifest.read_text())
        published = f"{manifest['controller']['repository']}@{manifest['controller']['digest']}"
        if parsed.controller or parsed.workspace:
            if aws("sts", "get-caller-identity")["Account"] != ACCOUNT:
                raise ValueError("AWS credentials are outside the testing account")
        if parsed.controller:
            if not re.fullmatch(re.escape(REGISTRY) + "@" + DIGEST, parsed.controller):
                raise ValueError("controller must be an exact testing ECR digest")
            digest = parsed.controller.split("@", 1)[1]
            images = aws("ecr", "describe-images", "--registry-id", ACCOUNT, "--repository-name", REPOSITORY,
                         "--image-ids", f"imageDigest={digest}")["imageDetails"]
            if len(images) != 1 or images[0]["imageDigest"] != digest:
                raise ValueError("controller digest is missing from testing ECR")
        ami = workspace_pin(json.loads(parsed.workspace.read_text()), manifest["runtimeCompatibility"]) if parsed.workspace else None
        catalog = parsed.regional_catalog.read_bytes() if parsed.one_pass else b""
        rendered = render_development(base, published, parsed.controller, ami, one_pass=parsed.one_pass, starter_catalog=catalog)
        with parsed.output.open("x") as destination:
            json.dump(rendered, destination, indent=2)
            destination.write("\n")
    except (KeyError, OSError, StopIteration, TypeError, ValueError, yaml.YAMLError, subprocess.CalledProcessError) as error:
        args.exit(1, f"ShiftBox development: {error}\n")


if __name__ == "__main__":
    main()
