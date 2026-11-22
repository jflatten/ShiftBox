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
ami=$(jq -r .amiId <<<"$workspace")
root=$(jq -r .rootDeviceName <<<"$workspace")
snapshot=$(jq -r .rootSnapshotId <<<"$workspace")
case "$service:$operation" in
  sts:get-caller-identity) printf '%s\n' "${MOCK_ACCOUNT:-$account}" ;;
  ec2:describe-instance-type-offerings)
    printf '{"InstanceTypeOfferings":[{"Location":"%sa","InstanceType":"t3.medium"},{"Location":"%sa","InstanceType":"t4g.micro"}]}\n' "$region" "$region"
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
    if [[ $name == "$run-dns-zones" ]]; then
      [[ $region == us-east-2 && $(flags --stack-name) == "$stack" && $(flags --change-set-type) == UPDATE ]]
    else
      [[ $name == "$run-foundation" && $(flags --stack-name) == "$run" && $(flags --change-set-type) == CREATE ]]
    fi
    parameters=$(flags --parameters)
    file="$FIXTURE/parameters-$region-$name.json"
    cp "${parameters#file://}" "$file"
    jq -e --arg phase "${name#"$run-"}" '
      any(.[]; .ParameterKey == "RoleBoundaryMode" and .ParameterValue == "external") and
      any(.[]; .ParameterKey == "Phase" and .ParameterValue == $phase)' "$file" >/dev/null
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
        Changes:(if $name == ($run+"-dns-zones") then
          [resource("PrivateZone";"AWS::Route53::HostedZone"), resource("DiscoveryNamespace";"AWS::ServiceDiscovery::PrivateDnsNamespace")]
          + (if $unexpected == "early-record" then [resource("PrivateRecords";"AWS::Route53::RecordSetGroup")] else [] end)
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
      jq -n --arg stack "$stack" --arg run "$run" --arg role "$SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN" '
        {Stacks:[{StackId:$stack,StackName:$run,StackStatus:"UPDATE_COMPLETE",EnableTerminationProtection:true,RoleARN:$role,
          Capabilities:["CAPABILITY_NAMED_IAM"],
          Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
          Parameters:[{ParameterKey:"RoleBoundaryMode",ParameterValue:"managed"},{ParameterKey:"DnsSuffix",ParameterValue:($run+".internal")}],
          Outputs:({Phase:"active",VpcId:"vpc-00000000000000001",PrivateZoneId:"Z0PRIVATE",
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
          {OutputKey:"VpcId",OutputValue:"vpc-00000000000000001"}]}]}'
    ;;
  cloudformation:list-stack-resources)
    if [[ ${MOCK_STAFFED_STACK:-false} == true ]]; then
      jq -n --arg service "${MOCK_DISCOVERY_SERVICE:-srv-0123456789abcdef}" '
        def r($type; $id): {ResourceType:$type,PhysicalResourceId:$id,ResourceStatus:"CREATE_COMPLETE"};
        {StackResourceSummaries:({
          Vpc:r("AWS::EC2::VPC";"vpc-00000000000000001"),
          PrivateZone:r("AWS::Route53::HostedZone";"Z0PRIVATE"),
          PrivateRecords:r("AWS::Route53::RecordSetGroup";"Z0PRIVATE"),
          DiscoveryNamespace:r("AWS::ServiceDiscovery::PrivateDnsNamespace";"ns-abcdefghijklmnop"),
          PostgresDiscovery:r("AWS::ServiceDiscovery::Service";"srv-abcdefghijklmnop"),
          ControllerDiscovery:r("AWS::ServiceDiscovery::Service";$service)
        } | to_entries | map(.value + {LogicalResourceId:.key}))}'
      exit
    fi
    printf '{"StackResourceSummaries":[{"LogicalResourceId":"InstallationVolume","PhysicalResourceId":"vol-00000000000000001"},{"LogicalResourceId":"Vpc","PhysicalResourceId":"vpc-00000000000000001","ResourceType":"AWS::EC2::VPC"}]}\n'
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
# Successful fixtures run precisely four isolated regional probes, remove review stacks, and execute only the Ohio foundation.
: >"$fixture/aws.log"
: >"$fixture/terraform.log"
deploy
[[ $(grep -c 'create-change-set' "$fixture/aws.log") = 5 ]]
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 3 ]]
[[ $(grep -c '^us-east-2 cloudformation execute-change-set' "$fixture/aws.log") = 1 ]]
grep -q '^us-east-2 cloudformation delete-change-set .*sbq-123-1-dns-zones/' "$fixture/aws.log"
if grep -q 'execute-change-set .*dns-zones' "$fixture/aws.log"; then exit 1; fi
[[ $(grep -c 'plan .* -destroy\|plan -destroy' "$fixture/terraform.log") = 4 ]]
jq -e '.status == "automated-foundation-only" and (.regions | length == 4) and
  all(.regions[]; .amiBooted and .hostedChangeSetValidated and .terraformDestroyCompleted) and
  .dnsZoneChangeSetReviewed == true and .dnsBootstrapExecuted == false and
  .manualGatesPending == true' "$fixture/evidence/automated.json" >/dev/null

# DNS records arriving before the zone and service ownership handoff cannot execute.
rm -- "$fixture/evidence/automated.json"
: >"$fixture/aws.log"
reject env MOCK_DNS_STAGE=early-record bash "$here/deploy.sh" "$fixture/manifest.json" "$fixture/assets" sbq-123-1 "$fixture/evidence"
grep -q 'change set' "$fixture/rejected.log"
if grep -q 'execute-change-set .*dns-zones' "$fixture/aws.log"; then exit 1; fi
[[ ! -e "$fixture/evidence/automated.json" ]]

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
jq -e 'has("staffedDnsInventoryVerified") | not' "$fixture/evidence/automated.json" >/dev/null
check_staffed
jq -e '.staffedDnsInventoryVerified and .manualGatesPending and (.dnsBootstrapExecuted | not)' "$fixture/evidence/automated.json" >/dev/null
if grep -E 'create-change-set|execute-change-set|delete-stack|delete-change-set' "$fixture/aws.log"; then exit 1; fi
printf 'Qualification deployment fixtures passed.\n'
