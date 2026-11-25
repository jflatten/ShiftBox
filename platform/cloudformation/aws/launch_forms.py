"""Render the console forms from the same stack used by the CLI and qualification."""

from copy import deepcopy
from pathlib import Path
import re
import runpy
import yaml


SOURCE = Path(__file__).resolve().parent
FORMS = ("private", "public", "public-placement")
NO_VALUE = {"Ref": "AWS::NoValue"}


class CloudFormationLoader(yaml.SafeLoader):
    """Preserve CloudFormation expressions as their JSON long forms."""


def intrinsic(loader, tag, node):
    if isinstance(node, yaml.SequenceNode):
        value = loader.construct_sequence(node, deep=True)
    elif isinstance(node, yaml.MappingNode):
        value = loader.construct_mapping(node, deep=True)
    else:
        value = loader.construct_scalar(node)
    if tag == "GetAtt" and isinstance(value, str):
        value = value.split(".", 1)
    return {tag if tag in ("Ref", "Condition") else f"Fn::{tag}": value}


CloudFormationLoader.add_multi_constructor("!", intrinsic)


def ref(name):
    return {"Ref": name}


def attr(resource, name):
    return {"Fn::GetAtt": [resource, name]}


def label(name):
    return {"default": name}


def configure_launch_form(base, mode, *, one_pass_ready=False):
    if mode not in FORMS:
        raise ValueError(f"Unknown launch form {mode}")
    template = deepcopy(base)
    if not one_pass_ready:
        configure = runpy.run_path(str(SOURCE.parents[1] / "development/one_pass.py"))["configure_one_pass"]
        configure(template, namespaced_workspaces=False)
    managed = mode != "private"
    placement = mode == "public-placement"
    fixed = {"ManagedBrowserTLS": managed, "PrivateBootstrap": not managed, "CreateManagedPrivateZone": False}
    if not managed:
        fixed.update(PublicEnabled=False, CreateManagedPublicZone=False)
    replacements = {
        "DeploymentName": ref("AWS::StackName"), "DnsSuffix": attr("LaunchDiscovery", "DnsSuffix"),
        "VpcId": attr("LaunchDiscovery", "VpcId"), "AvailabilityZone": attr("LaunchDiscovery", "AvailabilityZone"),
        "InitializeDataVolume": "true",
    }
    if managed:
        replacements.update(BaseDomain=attr("LaunchDiscovery", "BaseDomain"), PublicSubnet=attr("LaunchDiscovery", "PublicSubnet"),
                            PublicHostedZoneId=ref("PublicDnsZone"), PublicParentHostedZoneId=ref("PublicDnsZone"))

    # Cloud Map can reuse a namespace ID when a stack with the same name is recreated.
    # Use the stack UUID for the routing targets behind the stable private hostnames.
    namespace_domain = {"Fn::Sub": ["svc-${StackInstanceId}.${DnsSuffix}", {
        "StackInstanceId": {"Fn::Select": [2, {"Fn::Split": ["/", ref("AWS::StackId")]}]},
        "DnsSuffix": replacements["DnsSuffix"],
    }]}

    def resolve(value):
        if isinstance(value, list):
            return [result for item in value if (result := resolve(item)) != NO_VALUE]
        if not isinstance(value, dict):
            return value
        if "Ref" in value:
            return replacements.get(value["Ref"], value)
        if "Fn::If" in value:
            name, enabled, disabled = value["Fn::If"]
            if name in fixed:
                return resolve(enabled if fixed[name] else disabled)
            return {"Fn::If": [name, resolve(enabled), resolve(disabled)]}
        if "Fn::Sub" in value:
            spec = value["Fn::Sub"]
            pattern, bindings = spec if isinstance(spec, list) else (spec, {})
            if pattern == "services.${DnsSuffix}":
                return namespace_domain
            bindings = resolve(bindings)
            if pattern.endswith(".services.${DnsSuffix}."):
                pattern = pattern.replace("services.${DnsSuffix}", "${ServiceDiscoveryDomain}")
                bindings["ServiceDiscoveryDomain"] = namespace_domain
            for var in re.findall(r"\$\{([^}]+)\}", pattern):
                if var in replacements and var not in bindings:
                    bindings[var] = replacements[var]
            return {"Fn::Sub": [pattern, bindings] if bindings else pattern}
        return {key: result for key, item in value.items() if (result := resolve(item)) != NO_VALUE}

    if managed:
        private_zone = attr("PrivateBrowserZone", "ZoneId")
        template["Resources"]["ManagedPrivateBrowserRecords"]["Properties"]["HostedZoneId"] = private_zone
        template["Outputs"]["PrivateBrowserDnsZoneId"]["Value"] = private_zone
    for section in ("Resources", "Outputs"):
        for key, value in list(template[section].items()):
            condition = value.get("Condition")
            if condition in fixed:
                if not fixed[condition]:
                    del template[section][key]
                    continue
                del value["Condition"]
            template[section][key] = resolve(value)
    for condition in fixed:
        template["Conditions"].pop(condition, None)
    if managed:
        template["Conditions"]["PublicEnabled"] = {"Fn::Equals": [ref("BrowserAccess"), "Public"]}
        template["Conditions"]["CreateManagedPublicZone"] = {"Fn::Equals": [ref("DnsPlacement"), "Delegate a child zone"]}
    template["Description"] = f"ShiftBox {mode} launch. Create one stack, then finish setup in the browser."

    old = template["Parameters"]
    parameters = {key: old[key] for key in ("ServiceSubnet", "WorkspaceSubnet", "DnsSuffix", "OperatorCidr",
                                            "DataVolumeSizeGiB", "OciImageCacheSizeGiB")}
    parameters["DnsSuffix"].pop("MinLength")
    parameters["DnsSuffix"].update(Default="", AllowedPattern="(|" + old["DnsSuffix"]["AllowedPattern"] + ")",
                                   Description="Leave blank to use the stack name followed by .internal.")
    if not managed:
        parameters["OperatorCidr"].pop("Default")
        parameters["OperatorCidr"]["AllowedPattern"] = old["OperatorCidr"]["AllowedPattern"][2:-1]
        parameters["OperatorCidr"]["Description"] = "IPv4 range that can reach the private browser URL. Use /32 for one address."
    else:
        parameters.update({key: old[key] for key in ("PublicIngressCidr", "AcmeContactEmail", "AcmeTermsAccepted")})
        parameters["BrowserAccess"] = {"Type": "String", "AllowedValues": ["Public", "Private"], "Default": "Private",
                                       "Description": "Choose Public to enable an internet-facing endpoint. Private keeps the same domain accessible through the VPC."}
        parameters["PublicDnsZone"] = {"Type": "AWS::Route53::HostedZone::Id",
                                       "Description": "Select the authoritative public Route 53 hosted zone in this account."}
        parameters["DnsPlacement"] = {"Type": "String", "AllowedValues": ["Use selected zone", "Delegate a child zone"],
                                      "Default": "Use selected zone", "Description": "The stack can delegate a new child zone under the selected zone."}
        if placement:
            parameters["PublicSubnet"] = {"Type": "AWS::EC2::Subnet::Id",
                                          "Description": "Select the Internet-routed subnet in the service subnet's VPC and Availability Zone."}
    template["Parameters"] = parameters
    interface = template["Metadata"]["AWS::CloudFormation::Interface"]
    network = ["ServiceSubnet", "WorkspaceSubnet"]
    browser = ["OperatorCidr"]
    advanced = ["DnsSuffix", "DataVolumeSizeGiB", "OciImageCacheSizeGiB"]
    if managed:
        if placement:
            network.append("PublicSubnet")
        browser = ["BrowserAccess", "PublicDnsZone", "PublicIngressCidr", "AcmeContactEmail", "AcmeTermsAccepted"]
        advanced[0:0] = ["DnsPlacement", "OperatorCidr"]
    interface["ParameterGroups"] = [{"Label": label(group), "Parameters": fields} for group, fields in
                                    (("Network", network), ("Browser access", browser), ("Advanced", advanced))]
    interface["ParameterLabels"].update({
        "ServiceSubnet": label("Service subnet"), "WorkspaceSubnet": label("Workspace subnet"),
        "DnsSuffix": label("Private DNS suffix"), "OperatorCidr": label("Private browser source IPv4 range"),
    })
    if managed:
        interface["ParameterLabels"].update({"BrowserAccess": label("Browser access"),
                                             "DnsPlacement": label("DNS placement"),
                                             "PublicDnsZone": label("Public Route 53 hosted zone"),
                                             "PublicIngressCidr": label("Public browser source IPv4 range"),
                                             "AcmeContactEmail": label("Let’s Encrypt contact email"),
                                             "AcmeTermsAccepted": label("Accept Let’s Encrypt subscriber agreement")})
        if placement:
            interface["ParameterLabels"]["PublicSubnet"] = label("Public subnet")
    for key in ("SubnetsInSelectedVpc", "PublicAccessInputs", "ManagedBrowserInputs"):
        del template["Rules"][key]
    template["Rules"]["SameVpc"] = {"Assertions": [{
        "Assert": {"Fn::EachMemberEquals": [[{"Fn::ValueOf": ["WorkspaceSubnet", "VpcId"]}],
                                             {"Fn::ValueOf": ["ServiceSubnet", "VpcId"]}]},
        "AssertDescription": "Select service and workspace subnets from the same VPC.",
    }]}
    if managed:
        for name, message in (("AcmeContactEmail", "Enter a Let’s Encrypt contact email."),
                              ("AcmeTermsAccepted", "Accept the Let’s Encrypt subscriber agreement.")):
            accepted = {"Fn::Equals": [ref(name), "true" if name == "AcmeTermsAccepted" else ""]}
            template["Rules"][name + "Required"] = {"Assertions": [{"Assert": accepted if name == "AcmeTermsAccepted"
                                                                   else {"Fn::Not": [accepted]}, "AssertDescription": message}]}
        for access, field in (("Public", "PublicIngressCidr"), ("Private", "OperatorCidr")):
            template["Rules"][access + "SourceRequired"] = {
                "RuleCondition": {"Fn::Equals": [ref("BrowserAccess"), access]},
                "Assertions": [{"Assert": {"Fn::Not": [{"Fn::Equals": [ref(field), ""]}]},
                                "AssertDescription": f"Enter an allowed {access.lower()} browser source IPv4 range."}],
            }
    template["Resources"].update(discovery_resources(managed, placement))
    return template


def discovery_resources(managed, placement):
    region = ref("AWS::Region")
    role_name = {"Fn::Sub": "${AWS::StackName}-${AWS::Region}-discovery"}
    tags = [{"Key": "shiftbox-project", "Value": "shiftbox"}, {"Key": "shiftbox-cluster-name", "Value": ref("AWS::StackName")}]
    grants = [
        {"Effect": "Allow", "Action": ["ec2:DescribeSubnets"] + (["ec2:DescribeRouteTables"] if managed else []), "Resource": "*",
         "Condition": {"StringEquals": {"aws:RequestedRegion": region}}},
        {"Effect": "Allow", "Action": ["logs:CreateLogStream", "logs:PutLogEvents"],
         "Resource": {"Fn::Sub": "arn:${AWS::Partition}:logs:${AWS::Region}:${AWS::AccountId}:log-group:${DiscoveryLogs}:log-stream:*"}},
    ]
    if managed:
        grants.extend([
            # Route 53 also checks these EC2 read permissions for private VPC zones.
            {"Effect": "Allow", "Action": ["ec2:DescribeRegions", "ec2:DescribeVpcs"], "Resource": "*"},
            {"Effect": "Allow", "Action": "route53:ListHostedZonesByVPC", "Resource": "*"},
            {"Effect": "Allow", "Action": "route53:GetChange",
             "Resource": {"Fn::Sub": "arn:${AWS::Partition}:route53:::change/*"}},
            {"Effect": "Allow", "Action": ["route53:GetHostedZone", "route53:ChangeTagsForResource", "route53:DeleteHostedZone"],
             "Resource": {"Fn::Sub": "arn:${AWS::Partition}:route53:::hostedzone/*"}},
            {"Effect": "Allow", "Action": "route53:CreateHostedZone", "Resource": "*",
             "Condition": {"ForAllValues:StringLike": {"route53:VPCs": {"Fn::Sub": "VPCId=vpc-*,VPCRegion=${AWS::Region}"}},
                           "Null": {"route53:VPCs": "false"}}},
        ])
    policy = {"Version": "2012-10-17", "Statement": grants}
    context = {"com.aws.cloudformation.Context": {"why": "Resolve launch form choices before dependent resources are created.",
                                                   "must": ["Preserve adopted zones and zones with surviving records."]}}
    launch_props = {"ServiceToken": attr("DiscoveryFunction", "Arn"), "ServiceTimeout": "180",
                    "DeploymentName": ref("AWS::StackName"), "ServiceSubnet": ref("ServiceSubnet"),
                    "WorkspaceSubnet": ref("WorkspaceSubnet"), "DnsSuffix": ref("DnsSuffix")}
    if managed:
        launch_props.update(PublicZone=ref("PublicDnsZone"), PublicAccess={"Fn::If": ["PublicEnabled", "true", "false"]})
        if placement:
            launch_props["PublicSubnet"] = ref("PublicSubnet")
    code = (SOURCE / "launch_discovery.py").read_text() + '''

import cfnresponse


def handler(event, context):
    respond_to_cloudformation(event, context, cfnresponse)
'''
    resources = {
        "DiscoveryLogs": {"Type": "AWS::Logs::LogGroup", "Properties": {
            "LogGroupName": {"Fn::Sub": "/aws/lambda/${AWS::StackName}-${AWS::Region}-discovery"}, "RetentionInDays": 14, "Tags": tags}},
        "DiscoveryBoundary": {"Type": "AWS::IAM::ManagedPolicy", "Properties": {"PolicyDocument": policy}},
        "DiscoveryRole": {"Type": "AWS::IAM::Role", "Properties": {
            "RoleName": role_name, "Path": {"Fn::Sub": "/shiftbox/${AWS::Region}/${AWS::StackName}/"},
            "PermissionsBoundary": ref("DiscoveryBoundary"), "Tags": tags,
            "AssumeRolePolicyDocument": {"Version": "2012-10-17", "Statement": [
                {"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": {"Service": "lambda.amazonaws.com"}},
            ]}, "Policies": [{"PolicyName": "ResolveLaunchInputs", "PolicyDocument": policy}]}},
        "DiscoveryFunction": {"Type": "AWS::Lambda::Function", "Metadata": context, "Properties": {
            "FunctionName": role_name, "Role": attr("DiscoveryRole", "Arn"), "Runtime": "python3.13",
            "Handler": "index.handler", "Timeout": 390 if managed else 120, "Tags": tags,
            "Code": {"ZipFile": code}}},
        "LaunchDiscovery": {"Type": "Custom::ShiftBoxLaunchDiscovery", "Metadata": context, "Properties": launch_props},
    }
    if managed:
        resources["PrivateBrowserZone"] = {"Type": "Custom::ShiftBoxPrivateBrowserZone", "Metadata": context, "Properties": {
            "ServiceToken": attr("DiscoveryFunction", "Arn"), "ServiceTimeout": "420", "DeploymentName": ref("AWS::StackName"),
            "VpcId": attr("LaunchDiscovery", "VpcId"), "BaseDomain": attr("LaunchDiscovery", "BaseDomain"),
        }}
    return resources
