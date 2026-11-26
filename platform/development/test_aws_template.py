"""Exercise private iteration pins without an AWS account or a release tag."""

from copy import deepcopy

import pytest

from aws_template import ACCOUNT, REGION, render_development, verify_workspace_image


AMI = "ami-0123456789abcdef0"
SOURCE = "ami-0123456789abcdef1"
DIGEST = "sha256:" + "a" * 64
PUBLIC_IMAGE = "ghcr.io/jflatten/shiftbox@" + DIGEST
PRIVATE_IMAGE = f"{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com/shiftbox-development@{DIGEST}"


@pytest.fixture
def template():
    """Model the real rendered task definitions and separate policy boundary."""
    config = '{"runtime":{"ec2":{"deployment":"${DeploymentName}"}}}'
    task = {"Type": "AWS::ECS::TaskDefinition", "Properties": {"ContainerDefinitions": [{
        "Image": PUBLIC_IMAGE, "Environment": [{"Name": "SHIFTBOX_CLOUD_CONFIG", "Value": {"Fn::Sub": config}}],
    }]}}
    boundary = {"Type": "AWS::IAM::ManagedPolicy", "Properties": {"PolicyDocument": {"Statement": []}}}
    resources = {
        "SetupTask": deepcopy(task), "ControllerTask": deepcopy(task),
        "HostRoleBoundary": deepcopy(boundary), "ControllerTaskRoleBoundary": deepcopy(boundary),
        "ControllerTaskRole": {"Type": "AWS::IAM::Role", "Properties": {"Policies": [{
            "PolicyName": "OwnedWorkspaces", "PolicyDocument": {"Statement": []},
        }]}},
    }
    for name in ("Setup", "Controller", "Postgres"):
        role = f"{name}ExecutionRole"
        resources[role] = {"Type": "AWS::IAM::Role", "Properties": {"Policies": []}}
        resources[role + "Boundary"] = deepcopy(boundary)
        if name != "Postgres":
            resources[name + "Task"]["Properties"]["ExecutionRoleArn"] = {"Fn::GetAtt": [role, "Arn"]}
    return {"Mappings": {"ReleaseRegions": {REGION: {}, "us-east-1": {}}}, "Resources": resources}


def test_render_private_controller_and_ami_without_changing_public_template(template):
    # Development deploys only in the testing account and grants only the chosen image and repository.
    result = render_development(template, PUBLIC_IMAGE, PRIVATE_IMAGE, AMI)
    assert set(result["Mappings"]["ReleaseRegions"]) == {REGION}
    guards = result["Rules"]["DevelopmentDestination"]["Assertions"]
    assert guards[0]["Assert"]["Fn::Equals"] == [{"Ref": "AWS::AccountId"}, ACCOUNT]
    assert guards[1]["Assert"]["Fn::Equals"] == [{"Ref": "AWS::Region"}, REGION]
    resources = result["Resources"]
    # ECS pulls with the execution role and its boundary must permit the same actions.
    for task in ("SetupTask", "ControllerTask"):
        execution_role, _ = resources[task]["Properties"]["ExecutionRoleArn"]["Fn::GetAtt"]
        pull_policy, = resources[execution_role]["Properties"]["Policies"]
        grants = pull_policy["PolicyDocument"]["Statement"]
        assert grants == resources[execution_role + "Boundary"]["Properties"]["PolicyDocument"]["Statement"]
        token, repository = grants
        assert token == {"Effect": "Allow", "Action": "ecr:GetAuthorizationToken", "Resource": "*",
                         "Condition": {"StringEquals": {"aws:RequestedRegion": REGION}}}
        assert repository == {"Effect": "Allow",
                              "Action": ["ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"],
                              "Resource": f"arn:aws:ecr:{REGION}:{ACCOUNT}:repository/shiftbox-development"}
    for role in ("HostRoleBoundary", "PostgresExecutionRole", "PostgresExecutionRoleBoundary"):
        assert resources[role] == template["Resources"][role]
    role = resources["ControllerTaskRole"]["Properties"]["Policies"][0]["PolicyDocument"]["Statement"]
    assert role == resources["ControllerTaskRoleBoundary"]["Properties"]["PolicyDocument"]["Statement"]
    assert role[-1]["Resource"].endswith(f"::image/{AMI}")
    for name in ("SetupTask", "ControllerTask"):
        container = resources[name]["Properties"]["ContainerDefinitions"][0]
        assert container["Image"] == PRIVATE_IMAGE
        assert f'"verifiedSingleBootAmi":"{AMI}"' in container["Environment"][0]["Value"]["Fn::Sub"]
    assert template["Resources"]["SetupTask"]["Properties"]["ContainerDefinitions"][0]["Image"] == PUBLIC_IMAGE
    assert "DevelopmentDestination" not in template.get("Rules", {})


def test_baseline_deployment_retains_the_rc3_images_without_ecr_permissions(template):
    # A template-only review uses the original digest and does not grant access to private assets.
    result = render_development(template, PUBLIC_IMAGE)
    assert result["Resources"]["ControllerTask"]["Properties"]["ContainerDefinitions"][0]["Image"] == PUBLIC_IMAGE
    assert result["Resources"]["HostRoleBoundary"] == template["Resources"]["HostRoleBoundary"]
    assert result["Resources"]["ControllerTaskRoleBoundary"] == template["Resources"]["ControllerTaskRoleBoundary"]
    for role in ("SetupExecutionRole", "ControllerExecutionRole"):
        assert result["Resources"][role] == template["Resources"][role]
        assert result["Resources"][role + "Boundary"] == template["Resources"][role + "Boundary"]


@pytest.mark.parametrize("image", [PRIVATE_IMAGE.replace(ACCOUNT, "668112512962"), PRIVATE_IMAGE.split("@")[0] + ":latest"])
def test_render_rejects_other_registries_and_mutable_tags(template, image):
    # A publisher account or a mutable image cannot enter the test stack.
    with pytest.raises(ValueError, match="testing ECR digest"):
        render_development(template, PUBLIC_IMAGE, image, None)


def test_verify_private_ami_rejects_public_foreign_and_unencrypted_images():
    # A private development catalog binds its launch to the encrypted same-account AMI and build tags.
    build = {"releaseId": "dev-test", "sourceAmiId": SOURCE, "sourceRevision": "f" * 40}
    image = {"ImageId": AMI, "OwnerId": ACCOUNT, "Public": False, "State": "available",
             "Architecture": "x86_64", "RootDeviceName": "/dev/sda1", "RootDeviceType": "ebs",
             "VirtualizationType": "hvm", "BlockDeviceMappings": [{"DeviceName": "/dev/sda1", "Ebs": {
                 "Encrypted": True, "VolumeSize": 32, "SnapshotId": "snap-0123456789abcdef0"}}],
             "Tags": [{"Key": key, "Value": value} for key, value in {
                 "ShiftBoxArtifact": "workspace-ami", "ShiftBoxRelease": build["releaseId"],
                 "ShiftBoxSourceAMI": SOURCE, "ShiftBoxSourceCommit": build["sourceRevision"],
             }.items()]}
    verify_workspace_image(AMI, build, {"Images": [image]})
    for change in ({"Public": True}, {"OwnerId": "668112512962"}, {"Tags": []}):
        with pytest.raises(ValueError):
            verify_workspace_image(AMI, build, {"Images": [image | change]})
    image["BlockDeviceMappings"][0]["Ebs"]["Encrypted"] = False
    with pytest.raises(ValueError, match="encrypted"):
        verify_workspace_image(AMI, build, {"Images": [image]})


def test_one_pass_refuses_rc3_controller():
    # An immutable published controller cannot start the new private setup runtime.
    with pytest.raises(ValueError, match="private development controller"):
        render_development({}, PUBLIC_IMAGE, one_pass=True)


def test_current_template_starts_the_services_in_one_stack():
    # Fresh development installs exercise the same standalone form as release qualification.
    from pathlib import Path
    import yaml

    source = Path(__file__).parents[1] / "cloudformation" / "aws" / "template.yaml"
    template = yaml.safe_load(source.read_text())
    resources = template["Resources"]
    assert template["Parameters"]["BrowserAccess"]["Default"] == "Private"
    assert template["Parameters"]["ReleasePins"]["Type"] == "String"
    assert {"Phase", "PostgresCaPem"}.isdisjoint(template["Parameters"])
    assert {"SetupSession", "SetupTask"}.isdisjoint(resources)
    assert resources["ControllerTask"]["Properties"]["ContainerDefinitions"][0]["Command"] == ["one-pass"]
    assert resources["ControllerService"]["Properties"]["DesiredCount"] == 1
    assert resources["PostgresService"]["Properties"]["DesiredCount"] == 1
    interface = template["Metadata"]["AWS::CloudFormation::Interface"]
    cache_size = "OciImageCacheSizeGiB"
    assert template["Parameters"][cache_size]["Default"] == 20
    assert interface["ParameterLabels"][cache_size]["default"] == "OCI image cache size (GiB)"
    assert any(cache_size in group["Parameters"] for group in interface["ParameterGroups"])
    assert resources["RegistryCacheVolume"]["Properties"]["Size"] == {"Ref": cache_size}
    assert "RegistryCacheAttachment" in resources["ControllerService"]["DependsOn"]
    assert resources["EcsHost"]["Properties"]["NetworkInterfaces"][0]["AssociatePublicIpAddress"] is False
    assert resources["EcsHost"]["Properties"]["NetworkInterfaces"][0]["SubnetId"] == {"Ref": "ServiceSubnet"}
    assert all(resource["Type"] not in {"AWS::EC2::VPC", "AWS::EC2::Subnet", "AWS::EC2::NatGateway", "AWS::EC2::Route"}
               for resource in resources.values())
