"""Remove legacy stack inputs from the testing template for fresh installs."""


def configure_one_pass(template, *, namespaced_workspaces=True):
    """Use the shared one-pass bootstrap with stack-managed IAM and optional public HTTPS."""
    known = {
        "OnePassInstall": True,
        "StagedInstall": False,
        "ControllerEnabled": False,
        "ControllerRunning": True,
        "ManualSetupReady": False,
        "DnsZonesEnabled": True,
        "DnsServicesEnabled": True,
        "DatabaseEnabled": True,
        "UseExternalBoundaries": False,
        "UseManagedBoundaries": True,
        "UseManagedTaskBoundaries": True,
        "UseManagedSetupBoundaries": False,
        "ManageSetupOperatorPolicy": False,
    }
    no_value = {"Ref": "AWS::NoValue"}

    def resolve(value):
        if isinstance(value, list):
            return [result for item in value if (result := resolve(item)) != no_value]
        if isinstance(value, dict):
            conditional = value.get("Fn::If")
            if conditional:
                name, enabled, disabled = conditional
                if name in known:
                    return resolve(enabled if known[name] else disabled)
                return {"Fn::If": [name, resolve(enabled), resolve(disabled)]}
            return {(key.replace("Tag/ShiftBoxWorkspaceDeployment", "Tag/shiftbox-workspace-deployment")
                     if namespaced_workspaces else key): result
                    for key, item in value.items() if (result := resolve(item)) != no_value}
        return value

    for name in ("Phase", "PostgresCaPem", "RoleBoundaryMode"):
        del template["Parameters"][name]
    interface = template["Metadata"]["AWS::CloudFormation::Interface"]
    interface["ParameterGroups"] = [group for group in interface["ParameterGroups"]
                                   if "Phase" not in group["Parameters"]]
    for name in ("DatabaseTrustRequired", "OnePassInputs", "QualificationPhasesOnly"):
        del template["Rules"][name]
    assertions = template["Rules"]["ManagedBrowserInputs"]["Assertions"]
    assertions[:] = [item for item in assertions if item["Assert"] not in (
        {"Fn::Equals": [{"Ref": "Phase"}, "one-pass"]},
        {"Fn::Equals": [{"Ref": "RoleBoundaryMode"}, "managed"]},
    )]
    for name in known:
        del template["Conditions"][name]
    template["Conditions"]["PrivateBootstrap"] = {"Fn::Not": [{"Condition": "ManagedBrowserTLS"}]}
    del template["Outputs"]["Phase"]

    for section in ("Resources", "Outputs"):
        content = template[section]
        for name, value in list(content.items()):
            condition = value.get("Condition")
            if condition in known:
                if not known[condition]:
                    del content[name]
                    continue
                del value["Condition"]
            content[name] = resolve(value)

    if not namespaced_workspaces:
        return
    controller = template["Resources"]["ControllerTask"]["Properties"]["ContainerDefinitions"][0]
    runtime = next(entry["Value"]["Fn::Sub"] for entry in controller["Environment"]
                   if entry["Name"] == "SHIFTBOX_CLOUD_CONFIG")
    if runtime[0].count('"ec2":{') != 1:
        raise ValueError("expected one EC2 runtime configuration for namespaced workspace tags")
    runtime[0] = runtime[0].replace('"ec2":{', '"ec2":{"namespacedTags":true,', 1)
