#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
export SHIFTBOX_QUALIFICATION_ACCOUNT_ID=111122223333 SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=444455556666
export FIXTURE="$fixture"
export SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN=arn:aws:iam::111122223333:role/shiftbox-qualification-cloudformation
export PATH="$fixture/bin:$PATH"
mkdir -p "$fixture"/{bin,assets,support/catalog,support/docs,evidence}
printf 'Resources: verified-fixture\n' >"$fixture/assets/shiftbox-aws-cloudformation.yaml"
printf 'fixture only\n' >"$fixture/assets/install.sh"
printf '# Fixture\n' >"$fixture/support/docs/install.md"
printf '#!/bin/bash\n' >"$fixture/support/setup.sh"
cp "$here/../cloudformation/aws/stack-policy.json" "$fixture/support/stack-policy.json"
regions=(us-east-1 us-east-2 us-west-1 us-west-2)
export SHIFTBOX_QUALIFICATION_SUBNETS='{"us-east-1":{"service":"subnet-00000000000000001","workspace":"subnet-00000000000000002"},"us-east-2":{"service":"subnet-00000000000000001","workspace":"subnet-00000000000000002"},"us-west-1":{"service":"subnet-00000000000000001","workspace":"subnet-00000000000000002"},"us-west-2":{"service":"subnet-00000000000000001","workspace":"subnet-00000000000000002"}}'
for region in "${regions[@]}"; do
  printf '%s\n' "$region" >"$fixture/support/catalog/$region.json"
done
tar -czf "$fixture/assets/shiftbox-aws-support.tar.gz" -C "$fixture/support" \
  stack-policy.json setup.sh docs/install.md catalog/us-east-1.json catalog/us-east-2.json catalog/us-west-1.json catalog/us-west-2.json
hash() { openssl dgst -sha256 -r <"$1" | cut -d' ' -f1; }
catalog='{}'
for region in "${regions[@]}"; do
  catalog=$(jq -c --arg region "$region" --arg hash "$(hash "$fixture/support/catalog/$region.json")" '. + {($region):$hash}' <<<"$catalog")
done
jq -n --arg tag v1.0.0-rc.1 --arg commit 0123456789abcdef0123456789abcdef01234567 \
  --arg publisher 444455556666 --arg template "$(hash "$fixture/assets/shiftbox-aws-cloudformation.yaml")" \
  --arg installer "$(hash "$fixture/assets/install.sh")" --arg support "$(hash "$fixture/assets/shiftbox-aws-support.tar.gz")" \
  --argjson catalog "$catalog" -f "$here/../release/tests/distribution-fixture.jq" >"$fixture/manifest.json"

cat >"$fixture/bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
while (($#>1)); do
  if [[ $1 = --output ]]; then output=$2; shift; fi
  shift
done
[[ $1 == 'https://fixture-templates-'*.amazonaws.com/*'?versionId=2026%2Brc%2Fone%3D' ]]
cp "$FIXTURE/assets/shiftbox-aws-cloudformation.yaml" "$output"
MOCK
cat >"$fixture/bin/aws" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 = --region ]]
region=$2 service=$3 operation=$4
shift 4
args=("$@")
printf '%s %s %s %s\n' "$region" "$service" "$operation" "$*" >>"$FIXTURE/aws.log"
account=111122223333
run=sbq-123-1
stack="arn:aws:cloudformation:$region:$account:stack/$run/abcd1234-abcd-abcd-abcd-abcdef123456"
change_prefix="arn:aws:cloudformation:$region:$account:changeSet/$run"
flags() {
  local key=$1 current=0
  for value in "${args[@]}"; do
    if ((current)); then printf '%s\n' "$value"; return; fi
    [[ $value != "$key" ]] || current=1
  done
  return 1
}
workspace=$(jq -c --arg region "$region" '.regions[$region].workspaceImage' "$FIXTURE/manifest.json")
schema=$(jq -r .schemaVersion "$FIXTURE/manifest.json")
ami=$(jq -r .amiId <<<"$workspace")
root=$(jq -r .rootDeviceName <<<"$workspace")
snapshot=$(jq -r .rootSnapshotId <<<"$workspace")
case "$service:$operation" in
  sts:get-caller-identity) printf '%s\n' "${MOCK_ACCOUNT:-$account}" ;;
  ec2:describe-subnets)
    jq -n --arg region "$region" --arg account "$account" \
      --arg owner "${MOCK_SUBNET_OWNER:-$account}" '
      {Subnets:[{SubnetId:"subnet-00000000000000001",VpcId:"vpc-00000000000000001",OwnerId:$account,
        AvailabilityZone:($region+"a"),State:"available",MapPublicIpOnLaunch:false},
        {SubnetId:"subnet-00000000000000002",VpcId:"vpc-00000000000000001",OwnerId:$owner,
        AvailabilityZone:($region+"b"),State:"available",MapPublicIpOnLaunch:false}]}' ;;
  ec2:describe-instance-type-offerings)
    [[ $(flags --filters) == 'Name=instance-type,Values=t3.medium' ]]
    printf '{"InstanceTypeOfferings":[{"Location":"%sa","InstanceType":"t3.medium"}]}\n' "$region"
    ;;
  ec2:describe-images)
    if [[ $(flags --owners) = amazon ]]; then
      jq -n --arg host "$(flags --image-ids)" '
        {Images:[{ImageId:$host,State:"available",Architecture:"x86_64",RootDeviceType:"ebs",VirtualizationType:"hvm"}]}'
    else
      jq -n --argjson w "$workspace" --arg owner "${MOCK_OWNER:-444455556666}" '
        {Images:[{ImageId:$w.amiId,OwnerId:$owner,Public:true,State:"available",Architecture:"x86_64",
          RootDeviceType:"ebs",RootDeviceName:$w.rootDeviceName,VirtualizationType:"hvm",
          BlockDeviceMappings:[{DeviceName:$w.rootDeviceName,Ebs:{SnapshotId:$w.rootSnapshotId,VolumeSize:32}}]}]}'
    fi
    ;;
  ec2:describe-instances)
    jq -n --arg ami "$ami" --arg root "$root" --arg run "$run" '
      {Reservations:[{OwnerId:"111122223333",Instances:[{ImageId:$ami,InstanceId:"i-00000000000000001",
        State:{Name:"running"},RootDeviceName:$root,MetadataOptions:{HttpTokens:"required"},VpcId:"vpc-00000000000000001",
        Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:($run+"-p")}],
        BlockDeviceMappings:[{DeviceName:$root,Ebs:{VolumeId:"vol-00000000000000001",DeleteOnTermination:true}}]}]}]}'
    ;;
  ec2:describe-volumes)
    jq -n --arg snapshot "$snapshot" --arg root "$root" '
      {Volumes:[{SnapshotId:$snapshot,VolumeId:"vol-00000000000000001",Encrypted:true,VolumeType:"gp3",
        Attachments:[{InstanceId:"i-00000000000000001",Device:$root,State:"attached"}]}]}'
    ;;
  ec2:wait) [[ ${MOCK_BOOT_FAILED:-false} != true ]] ;;
  cloudformation:wait|cloudformation:validate-template|cloudformation:delete-change-set|cloudformation:delete-stack|cloudformation:execute-change-set|cloudformation:update-termination-protection) ;;
  cloudformation:create-change-set)
    [[ $(flags --template-url) == "https://fixture-templates-$region.s3.$region.amazonaws.com/"*'?versionId=2026%2Brc%2Fone%3D' ]]
    [[ $(flags --role-arn) == "$SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN" ]]
    [[ $(flags --capabilities) == CAPABILITY_NAMED_IAM ]]
    name=$(flags --change-set-name)
    change="$change_prefix-${name#"$run-"}/abcd1234-abcd-abcd-abcd-abcdef123456"
    [[ $(flags --stack-name) == "$run" && $(flags --change-set-type) == CREATE ]]
    if [[ $schema == 2 ]]; then [[ $name == "$run-one-pass" ]]; else [[ $name == "$run-foundation" ]]; fi
    parameters=$(flags --parameters)
    file="$FIXTURE/parameters-$region-$name.json"
    cp "${parameters#file://}" "$file"
    if [[ $name == "$run-one-pass" ]]; then
      jq -e --arg region "$region" --arg ami "$ami" '
        map({key:.ParameterKey,value:.ParameterValue}) | from_entries |
        keys == ["AcmeContactEmail","AcmeTermsAccepted","BrowserAccess","DnsSuffix","OperatorCidr",
          "PublicDnsZone","ReleasePins","ServiceSubnet","WorkspaceSubnet"] and
        .BrowserAccess == "Private" and .PublicDnsZone == "ZPUBLIC12" and
        .ServiceSubnet == "subnet-00000000000000001" and .WorkspaceSubnet == "subnet-00000000000000002" and
        (.ReleasePins | fromjson | .Region == $region and .WorkspaceAmi == $ami)
      ' "$file" >/dev/null
    else
      jq -e --arg phase "${name#"$run-"}" '
      any(.[]; .ParameterKey == "RoleBoundaryMode" and .ParameterValue == "external") and
      any(.[]; .ParameterKey == "VpcId" and .ParameterValue == "vpc-00000000000000001") and
      any(.[]; .ParameterKey == "ServiceSubnet" and .ParameterValue == "subnet-00000000000000001") and
      any(.[]; .ParameterKey == "WorkspaceSubnet" and .ParameterValue == "subnet-00000000000000002") and
      all(.[]; .ParameterKey != "AccessMode" and .ParameterKey != "CreateNatGateway") and
      all(.[]; .ParameterKey != "GitHubAppSecretArn") and
        any(.[]; .ParameterKey == "Phase" and .ParameterValue == $phase)' "$file" >/dev/null
    fi
    jq -n --arg stack "$stack" --arg change "$change" '{StackId:$stack,Id:$change}'
    ;;
  cloudformation:describe-change-set)
    change=$(flags --change-set-name)
    name=${change%/*}
    name=${name##*/}
    jq -n --arg change "$change" --arg stack "$stack" --arg run "$run" --arg name "$name" \
      --arg capability "${MOCK_CAPABILITY:-CAPABILITY_NAMED_IAM}" \
      --arg action "${MOCK_ACTION:-Add}" --arg unexpected "${MOCK_DNS_STAGE:-}" \
      --slurpfile parameters "$FIXTURE/parameters-$region-$name.json" '
      def resource($id; $type): {Type:"Resource",ResourceChange:{Action:$action,LogicalResourceId:$id,ResourceType:$type}};
      {ChangeSetId:$change,StackId:$stack,Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",
        ChangeSetName:$name,Capabilities:[$capability],Parameters:$parameters[0],
        Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
        Changes:(if $name == ($run+"-one-pass") then
          [resource("LaunchDiscovery";"Custom::ShiftBoxLaunchDiscovery"), resource("PrivateBrowserZone";"Custom::ShiftBoxPrivateBrowserZone"),
           resource("ManagedPrivateBrowserRecords";"AWS::Route53::RecordSetGroup"),
           resource("InstallationVolume";"AWS::EC2::Volume"), resource("SetupClaim";"AWS::SecretsManager::Secret"),
           resource("PostgresService";"AWS::ECS::Service"), resource("ControllerService";"AWS::ECS::Service")]
          + (if $unexpected == "public-lb" then [resource("PublicLoadBalancer";"AWS::ElasticLoadBalancingV2::LoadBalancer")] else [] end)
        else [resource("InstallationVolume";"AWS::EC2::Volume"), resource("HostRole";"AWS::IAM::Role")]
          + (if $unexpected == "foundation-zone" then [resource("PrivateZone";"AWS::Route53::HostedZone")] else [] end)
        end)}'
    ;;
  cloudformation:get-template)
    if [[ ${MOCK_CHANGED:-false} = true ]]; then printf '{"TemplateBody":"foreign"}\n'
    else jq -n --rawfile template "$FIXTURE/assets/shiftbox-aws-cloudformation.yaml" '{TemplateBody:$template}'; fi
    ;;
  cloudformation:describe-events)
    if [[ ${MOCK_INVALID:-false} = true ]]; then printf '{"OperationEvents":[{"EventType":"VALIDATION_ERROR"}]}\n'
    else printf '{"OperationEvents":[]}\n'; fi
    ;;
  cloudformation:describe-stacks)
    if [[ ${MOCK_STAFFED_STACK:-false} == true ]]; then
      if [[ $schema == 2 ]]; then
        jq -n --arg stack "$stack" --arg run "$run" --arg role "$SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN" \
          --arg foreign "${MOCK_FOREIGN_PINS:-false}" --slurpfile params "$FIXTURE/parameters-$region-$run-one-pass.json" '
          ($params[0] | map(if .ParameterKey == "ReleasePins" and $foreign == "true" then
            .ParameterValue |= (fromjson | .Region = "us-west-1" | tojson) else . end)) as $selected |
          {Stacks:[{StackId:$stack,StackName:$run,StackStatus:"CREATE_COMPLETE",EnableTerminationProtection:true,RoleARN:$role,
            Capabilities:["CAPABILITY_NAMED_IAM"],Parameters:$selected,
            Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
            Outputs:({VpcId:"vpc-00000000000000001",ServiceSubnetId:"subnet-00000000000000001",
              WorkspaceSubnetId:"subnet-00000000000000002",PrivateZoneId:"Z0PRIVATE",PrivateBrowserDnsZoneId:"Z0BROWSER",
              DiscoveryNamespaceId:"ns-abcdefghijklmnop",DiscoveryHostedZoneId:"Z0DISCOVERY",
              PostgresDiscoveryServiceId:"srv-abcdefghijklmnop",ControllerDiscoveryServiceId:"srv-0123456789abcdef"}
              | to_entries | map({OutputKey:.key,OutputValue:.value}))}]}'
        exit
      fi
      jq -n --arg stack "$stack" --arg run "$run" --arg role "$SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN" \
        --arg service "${MOCK_SERVICE_SUBNET:-subnet-00000000000000001}" '
        {Stacks:[{StackId:$stack,StackName:$run,StackStatus:"UPDATE_COMPLETE",EnableTerminationProtection:true,RoleARN:$role,
          Capabilities:["CAPABILITY_NAMED_IAM"],
          Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
          Parameters:[{ParameterKey:"RoleBoundaryMode",ParameterValue:"managed"},{ParameterKey:"DnsSuffix",ParameterValue:($run+".internal")},
            {ParameterKey:"VpcId",ParameterValue:"vpc-00000000000000001"},{ParameterKey:"ServiceSubnet",ParameterValue:$service},
            {ParameterKey:"WorkspaceSubnet",ParameterValue:"subnet-00000000000000002"},
            {ParameterKey:"AvailabilityZone",ParameterValue:"us-east-2a"}],
          Outputs:({Phase:"active",VpcId:"vpc-00000000000000001",ServiceSubnetId:$service,
            WorkspaceSubnetId:"subnet-00000000000000002",PrivateZoneId:"Z0PRIVATE",
            DiscoveryNamespaceId:"ns-abcdefghijklmnop",DiscoveryHostedZoneId:"Z0DISCOVERY",
            PostgresDiscoveryServiceId:"srv-abcdefghijklmnop",ControllerDiscoveryServiceId:"srv-0123456789abcdef"}
            | to_entries | map({OutputKey:.key,OutputValue:.value}))}]}'
      exit
    elif [[ $(flags --stack-name) == "$run" ]]; then
      printf 'An error occurred (ValidationError): Stack with id %s does not exist\n' "$run" >&2
      exit 1
    fi
    jq -n --arg stack "$stack" --arg run "$run" --arg role "$SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN" '
      {Stacks:[{StackId:$stack,StackName:$run,StackStatus:"CREATE_COMPLETE",EnableTerminationProtection:true,RoleARN:$role,
        Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
        Outputs:[{OutputKey:"Phase",OutputValue:"foundation"},{OutputKey:"InstallationVolumeId",OutputValue:"vol-00000000000000001"},
          {OutputKey:"VpcId",OutputValue:"vpc-00000000000000001"},
          {OutputKey:"ServiceSubnetId",OutputValue:"subnet-00000000000000001"},
          {OutputKey:"WorkspaceSubnetId",OutputValue:"subnet-00000000000000002"}]}]}'
    ;;
  cloudformation:list-stack-resources)
    if [[ ${MOCK_STAFFED_STACK:-false} == true ]]; then
      jq -n --arg service "${MOCK_DISCOVERY_SERVICE:-srv-0123456789abcdef}" \
        --arg router "${MOCK_ROUTER_RESOURCE:-false}" --arg schema "$schema" '
        def r($type; $id): {ResourceType:$type,PhysicalResourceId:$id,ResourceStatus:"CREATE_COMPLETE"};
        {StackResourceSummaries:(({
          PrivateZone:r("AWS::Route53::HostedZone";"Z0PRIVATE"),
          PrivateRecords:r("AWS::Route53::RecordSetGroup";"Z0PRIVATE"),
          DiscoveryNamespace:r("AWS::ServiceDiscovery::PrivateDnsNamespace";"ns-abcdefghijklmnop"),
          PostgresDiscovery:r("AWS::ServiceDiscovery::Service";"srv-abcdefghijklmnop"),
          ControllerDiscovery:r("AWS::ServiceDiscovery::Service";$service)
        } + (if $schema == "2" then {
          PrivateBrowserZone:r("Custom::ShiftBoxPrivateBrowserZone";"adopted:Z0BROWSER"),
          ManagedPrivateBrowserRecords:r("AWS::Route53::RecordSetGroup";"Z0BROWSER")
        } else {} end) | to_entries | map(.value + {LogicalResourceId:.key})) +
          (if $router == "true" then [r("AWS::EC2::Instance";"i-00000000000000001") + {LogicalResourceId:"TailscaleRouter"}] else [] end))}'
      exit
    fi
    printf '{"StackResourceSummaries":[{"LogicalResourceId":"InstallationVolume","PhysicalResourceId":"vol-00000000000000001"}]}\n'
    ;;
  cloudformation:set-stack-policy)
    policy=$(flags --stack-policy-body)
    cp "${policy#file://}" "$FIXTURE/policy.json"
    ;;
  cloudformation:get-stack-policy)
    jq -n --rawfile policy "$FIXTURE/policy.json" '{StackPolicyBody:$policy}'
    ;;
  *) printf 'Unexpected AWS command: %s %s\n' "$service" "$operation" >&2; exit 99 ;;
esac
MOCK
cat >"$fixture/bin/terraform" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
directory=${1#-chdir=}
shift
region=${directory##*/}
printf '%s %s\n' "$region" "$*" >>"$FIXTURE/terraform.log"
case "$1:$*" in
  init:*) ;;
  plan:*-destroy*) ;;
  plan:*)
    jq -e --arg region "$region" '
      .customer_account_id == "111122223333" and .publisher_account_id == "444455556666" and
      .name == "sbq-123-1-p" and .aws_region == $region' "$directory/probe.auto.tfvars.json" >/dev/null
    ;;
  apply:*) ;;
  output:*instance_id) printf 'i-00000000000000001\n' ;;
  output:*root_volume_id) printf 'vol-00000000000000001\n' ;;
  *) exit 99 ;;
esac
MOCK
chmod +x "$fixture/bin/"*

deploy() {
  bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" "${1:-sbq-123-1}" "$fixture/evidence"
}
reject() {
  if "$@" >"$fixture/rejected.log" 2>&1; then
    printf 'Unexpected success for rejected qualification fixture\n' >&2
    exit 1
  fi
}
# The account fence and run naming stop before any AWS write.
reject env MOCK_ACCOUNT=444455556666 bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
if grep -Eq 'create-change-set|execute-change-set' "$fixture/aws.log"; then exit 1; fi
reject deploy production
# A different publisher, live template drift, or failed AWS validation cannot launch a foundation or any probe.
for reason in 'MOCK_OWNER=199900000001' 'MOCK_CHANGED=true' 'MOCK_INVALID=true' 'MOCK_ACTION=Remove'; do
  reject env "$reason" bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
done
# The draft role must see the named capability and no DNS resources during foundation.
for reason in 'MOCK_CAPABILITY=CAPABILITY_IAM' 'MOCK_DNS_STAGE=foundation-zone'; do
  reject env "$reason" bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
done
if grep -q 'execute-change-set' "$fixture/aws.log"; then exit 1; fi
[[ ! -e "$fixture/terraform.log" ]]
# If a billable probe was launched and did not boot, its own destroy plan still runs.
reject env MOCK_BOOT_FAILED=true bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
grep -q 'apply .* destroy.tfplan' "$fixture/terraform.log"
[[ ! -e "$fixture/evidence/automated.json" ]]
# An unowned subnet prevents all CloudFormation change sets and probe launches.
: >"$fixture/aws.log"
reject env MOCK_SUBNET_OWNER=444455556666 bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
grep -q 'requires existing private subnets' "$fixture/rejected.log"
if grep -q 'create-change-set' "$fixture/aws.log"; then exit 1; fi

# Legacy candidates still get four isolated probes and review stacks that automation never executes.
: >"$fixture/aws.log"
: >"$fixture/terraform.log"
deploy
[[ $(grep -c 'create-change-set' "$fixture/aws.log") = 4 ]]
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 4 ]]
if grep -q 'execute-change-set' "$fixture/aws.log"; then exit 1; fi
[[ $(grep -c 'plan .* -destroy\|plan -destroy' "$fixture/terraform.log") = 4 ]]
jq -e '.status == "automated-preview-only" and (.regions | length == 4) and
  all(.regions[]; .amiBooted and .hostedChangeSetValidated and .terraformDestroyCompleted) and
  .dnsZoneChangeSetReviewed == false and .dnsBootstrapExecuted == false and
  .manualGatesPending == true' "$fixture/evidence/automated.json" >/dev/null

rm -- "$fixture/evidence/automated.json"

# Staffed qualification needs a fresh Ohio console launch, so delete the Ohio
# review stack instead of executing it under the automation identity.
: >"$fixture/aws.log"
reject env SHIFTBOX_QUALIFICATION_STAFFED=true bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
[[ ! -s "$fixture/aws.log" ]]
SHIFTBOX_QUALIFICATION_STAFFED=true \
  SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN=arn:aws:iam::111122223333:role/shiftbox-qualification-staffed-cloudformation deploy
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 4 ]]
if grep -q 'execute-change-set' "$fixture/aws.log"; then exit 1; fi
jq -e '.status == "ohio-console-launch-pending" and
  .ohioFoundationDeployed == false and .dnsZoneChangeSetReviewed == false and
  .dnsBootstrapExecuted == false and .manualGatesPending == true' "$fixture/evidence/automated.json" >/dev/null

# The staffed readback must reject a foreign discovery ID and cannot report
# full browser qualification even when the exact candidate's DNS is present.
export SHIFTBOX_QUALIFICATION_STAFFED=true
export SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN=arn:aws:iam::111122223333:role/shiftbox-qualification-staffed-cloudformation
export MOCK_STAFFED_STACK=true
check_staffed() {
  bash "$here/deploy.sh" --verify-staffed-dns "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
}
: >"$fixture/aws.log"
MOCK_DISCOVERY_SERVICE=srv-0000000000000000 reject check_staffed
# Reject a subnet other than the admitted choice and any stack-created router.
MOCK_SERVICE_SUBNET=subnet-00000000000000003 reject check_staffed
grep -q 'reviewed role, parameters or ownership' "$fixture/rejected.log"
MOCK_ROUTER_RESOURCE=true reject check_staffed
grep -q 'does not expose the verified DNS inventory' "$fixture/rejected.log"
jq -e 'has("staffedDnsInventoryVerified") | not' "$fixture/evidence/automated.json" >/dev/null
check_staffed
jq -e '.staffedDnsInventoryVerified and .manualGatesPending and (.dnsBootstrapExecuted | not)' "$fixture/evidence/automated.json" >/dev/null
if grep -E 'create-change-set|execute-change-set|delete-stack|delete-change-set' "$fixture/aws.log"; then exit 1; fi

# A schema 2 candidate reviews the exact single template in each region, using
# manifest pins and the tested form fields, without executing a stack in automation.
unset MOCK_STAFFED_STACK SHIFTBOX_QUALIFICATION_STAFFED
rm "$fixture/evidence/automated.json"
source "$here/../release/manifest.sh"
jq "$manifest_filter"' .schemaVersion = 2 | . as $release |
  .regions |= with_entries(.key as $region | .value.templateLocation |=
    (.quickCreateUrl = release_console_url($release; $region; .url)))' \
  "$fixture/manifest.json" >"$fixture/one-pass-manifest.json"
mv "$fixture/one-pass-manifest.json" "$fixture/manifest.json"
: >"$fixture/aws.log"
reject deploy
grep -q 'qualification browser inputs' "$fixture/rejected.log"
if grep -q 'create-change-set' "$fixture/aws.log"; then exit 1; fi
export SHIFTBOX_QUALIFICATION_BROWSER='{"zoneId":"ZPUBLIC12","contactEmail":"operator@example.com","privateCidr":"10.24.8.0/24"}'
reject env MOCK_DNS_STAGE=public-lb bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
grep -q 'change set parameters, ownership or resource changes are invalid' "$fixture/rejected.log"
: >"$fixture/aws.log"
deploy
[[ $(grep -c 'create-change-set' "$fixture/aws.log") = 4 ]]
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 4 ]]
if grep -Eq 'execute-change-set|set-stack-policy' "$fixture/aws.log"; then exit 1; fi
jq -e '.status == "automated-preview-only" and .manualGatesPending and
  (.ohioFoundationDeployed | not) and (.regions | length == 4)' "$fixture/evidence/automated.json" >/dev/null

# Staffed readback accepts CREATE_COMPLETE with the pinned one-pass values and
# rejects a deployment whose release pins were changed after the handoff.
rm "$fixture/evidence/automated.json"
export SHIFTBOX_QUALIFICATION_STAFFED=true
deploy
export MOCK_STAFFED_STACK=true
MOCK_FOREIGN_PINS=true reject check_staffed
grep -q 'reviewed role, parameters or ownership' "$fixture/rejected.log"
check_staffed
jq -e '.staffedDnsInventoryVerified and .manualGatesPending' "$fixture/evidence/automated.json" >/dev/null
printf 'Qualification deployment fixtures passed.\n'
