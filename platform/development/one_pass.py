"""Compose private first login from the same stack resources as the RC3 baseline."""

from copy import deepcopy
from pathlib import Path


def _mount(task, container, name, directory, target):
    task["Volumes"].append({"Name": name, "Host": {"SourcePath": f"/var/lib/shiftbox-data/{directory}"}})
    container["MountPoints"].append({"SourceVolume": name, "ContainerPath": target, "ReadOnly": True})


def _allow(role, boundary, statement):
    role["Policies"][0]["PolicyDocument"]["Statement"].append(statement)
    boundary["PolicyDocument"]["Statement"].append(deepcopy(statement))


def configure_one_pass(template):
    """Remove staging gates and bootstrap the regular ECS service on its private origin."""
    parameters = template["Parameters"]
    for name in ("Phase", "PostgresCaPem", "RoleBoundaryMode"):
        del parameters[name]
    parameters["OperatorCidr"].pop("Default")
    parameters["OperatorCidr"]["AllowedPattern"] = r"([0-9]{1,3}\.){3}[0-9]{1,3}/([1-9]|[12][0-9]|3[0-2])"
    parameters["OperatorCidr"]["Description"] = (
        "This inbound rule controls who can open the ShiftBox UI over HTTPS on port 8443. "
        "Enter an IPv4 range, or use /32 for one address, such as 172.31.12.34/32."
    )
    parameters["ServiceSubnet"]["Description"] = "Existing subnet for ECS and PostgreSQL. ShiftBox disables public IPv4 assignment."
    parameters["WorkspaceSubnet"]["Description"] = "Existing workspace subnet in the same VPC. Workspaces use private IPv4."
    template["Metadata"]["AWS::CloudFormation::Interface"]["ParameterGroups"] = [
        {"Label": {"default": "Installation"},
         "Parameters": ["DeploymentName", "DataVolumeSizeGiB", "OciImageCacheSizeGiB", "InitializeDataVolume"]},
        {"Label": {"default": "Existing network"},
         "Parameters": ["VpcId", "ServiceSubnet", "WorkspaceSubnet", "AvailabilityZone", "DnsSuffix", "OperatorCidr"]},
    ]
    del template["Rules"]["DatabaseTrustRequired"]
    del template["Conditions"]

    resources = template["Resources"]
    for name in ("SetupGroup", "PostgresSetupIngress", "SetupExecutionRoleBoundary", "SetupTaskRoleBoundary",
                 "SetupExecutionRole", "SetupTaskRole", "SetupTask", "SetupSession", "SetupOperatorPolicy", "PostgresTlsSecret"):
        del resources[name]
    for name in ("Phase", "PostgresTlsSecretArn", "SetupOperatorPolicyArn", "SetupConfig"):
        del template["Outputs"][name]

    # Derive the normal managed-boundary path without the external qualification
    # branches. Role identity and permissions remain those of the original stack.
    def managed(value):
        if isinstance(value, list):
            return [managed(item) for item in value]
        if isinstance(value, dict):
            conditional = value.get("Fn::If")
            if conditional and conditional[0] == "UseExternalBoundaries":
                return managed(conditional[2])
            return {key.replace("Tag/ShiftBoxWorkspaceDeployment", "Tag/shiftbox-workspace-deployment"): managed(item)
                    for key, item in value.items()}
        return value

    for name, resource in resources.items():
        resource.pop("Condition", None)
        resources[name] = managed(resource)
    for output in template["Outputs"].values():
        output.pop("Condition", None)
    template["Outputs"] = managed(template["Outputs"])
    resources["PostgresGroup"]["Properties"]["GroupDescription"] = "PostgreSQL reachable only from the controller task"

    tags = [{"Key": "shiftbox-project", "Value": "shiftbox"},
            {"Key": "shiftbox-cluster-name", "Value": {"Ref": "DeploymentName"}}]
    resources["SetupClaim"] = {
        "Type": "AWS::SecretsManager::Secret", "DeletionPolicy": "Retain", "UpdateReplacePolicy": "Retain",
        "Properties": {"Description": "Initial private ShiftBox administrator claim code",
                       "GenerateSecretString": {"PasswordLength": 48, "ExcludePunctuation": True}, "Tags": deepcopy(tags)},
    }
    resources["BrowserTrust"] = {
        "Type": "AWS::SecretsManager::Secret", "DeletionPolicy": "Retain", "UpdateReplacePolicy": "Retain",
        "Properties": {"Description": "Public browser trust certificate for this private ShiftBox installation", "Tags": deepcopy(tags)},
    }
    for logical_id, description, resource in (
        ("SetupClaimSecretArn", "Retrieve the initial setup claim code with your authorized AWS identity", "SetupClaim"),
        ("BrowserCaSecretArn", "Retrieve the public browser CA with your authorized AWS identity", "BrowserTrust"),
    ):
        template["Outputs"][logical_id] = {"Description": description, "Value": {"Ref": resource}}

    host = resources["EcsHost"]["Properties"]["UserData"]["Fn::Base64"]["Fn::Sub"]
    host[0] = host[0].replace("volume_serial=", """cat > /usr/local/libexec/shiftbox-installation-tls <<'TLS_BOOTSTRAP'
${TlsBootstrapScript}
TLS_BOOTSTRAP
chmod 0755 /usr/local/libexec/shiftbox-installation-tls
volume_serial=""", 1)
    host[0] = host[0].replace("RemainAfterExit=yes", """ExecStartPost=/usr/local/libexec/shiftbox-installation-tls /var/lib/shiftbox-data ${DnsSuffix} ${AWS::Region} ${BrowserTrust}
RemainAfterExit=yes""", 1)
    host[1]["TlsBootstrapScript"] = Path(__file__).with_name("installation-tls.sh").read_text().rstrip("\n")
    # Cloud-init's expanded user data must fit EC2's 16 KiB raw limit.
    if len(host[0].encode()) + sum(len(part.encode()) for part in host[1].values()) + 1024 >= 16384:
        raise ValueError("one-pass host user data may exceed the EC2 limit")
    write_trust = {"Effect": "Allow", "Action": "secretsmanager:PutSecretValue", "Resource": {"Ref": "BrowserTrust"}}
    host_role = resources["HostRole"]["Properties"]
    host_role["Policies"] = [{"PolicyName": "PublishInstallationBrowserCA",
                              "PolicyDocument": {"Version": "2012-10-17", "Statement": [write_trust]}}]
    resources["HostRoleBoundary"]["Properties"]["PolicyDocument"]["Statement"].append(deepcopy(write_trust))

    postgres = resources["PostgresTask"]["Properties"]
    pg = postgres["ContainerDefinitions"][0]
    _mount(postgres, pg, "postgres-identity", "pg-identity", "/run/shiftbox/postgres-tls")
    pg["Secrets"] = [entry for entry in pg["Secrets"] if not entry["Name"].startswith("POSTGRES_TLS_")]
    pg["Command"][0] = pg["Command"][0].replace(
        """printf '%s\\n' "$POSTGRES_TLS_CERT" > "$tls/server.crt"
printf '%s\\n' "$POSTGRES_TLS_KEY" > "$tls/server.key" """.rstrip(),
        'cat /run/shiftbox/postgres-tls/server.crt > "$tls/server.crt"\n'
        'cat /run/shiftbox/postgres-tls/server.key > "$tls/server.key"',
    )
    if 'POSTGRES_TLS_CERT" >' in pg["Command"][0] or "/run/shiftbox/postgres-tls/server.key" not in pg["Command"][0]:
        raise ValueError("expected the common PostgreSQL TLS startup instructions")
    for name in ("PostgresExecutionRole", "PostgresExecutionRoleBoundary"):
        props = resources[name]["Properties"]
        policy = props["Policies"][0]["PolicyDocument"] if "Policies" in props else props["PolicyDocument"]
        policy["Statement"][0]["Resource"].remove({"Ref": "PostgresTlsSecret"})

    controller = resources["ControllerTask"]["Properties"]
    task = controller["ContainerDefinitions"][0]
    task["Command"] = ["one-pass"]
    task["LinuxParameters"] = {"InitProcessEnabled": True}
    task["Environment"] = [entry for entry in task["Environment"] if entry["Name"] != "SHIFTBOX_DB_CA"]
    runtime = next(entry["Value"]["Fn::Sub"] for entry in task["Environment"] if entry["Name"] == "SHIFTBOX_CLOUD_CONFIG")
    if runtime[0].count('"ec2":{') != 1:
        raise ValueError("expected one EC2 runtime configuration for namespaced workspace tags")
    runtime[0] = runtime[0].replace('"ec2":{', '"ec2":{"namespacedTags":true,', 1)
    _mount(controller, task, "browser-tls", "browser-identity", "/run/shiftbox/browser-tls")
    _mount(controller, task, "database-trust", "database-trust", "/run/shiftbox/database-trust")
    for secret, key in (("PostgresDexSecret", "SHIFTBOX_DEX_PASSWORD"), ("SetupClaim", "SHIFTBOX_BOOTSTRAP_CLAIM")):
        task["Secrets"].append({"Name": key, "ValueFrom": {"Ref": secret}})
    _allow(resources["ControllerExecutionRole"]["Properties"], resources["ControllerExecutionRoleBoundary"]["Properties"],
           {"Effect": "Allow", "Action": "secretsmanager:GetSecretValue",
            "Resource": [{"Ref": "PostgresDexSecret"}, {"Ref": "SetupClaim"}]})
    resources["ControllerService"]["Properties"]["DesiredCount"] = 1
    resources["PostgresService"]["DependsOn"].append("PostgresControllerIngress")
