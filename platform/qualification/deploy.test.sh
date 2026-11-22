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
change="arn:aws:cloudformation:$region:$account:changeSet/$run-foundation/abcd1234-abcd-abcd-abcd-abcdef123456"
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
    [[ $(flags --stack-name) == "$run" ]]
    parameters=$(flags --parameters)
    cp "${parameters#file://}" "$FIXTURE/parameters-$region.json"
    jq -n --arg stack "$stack" --arg change "$change" '{StackId:$stack,Id:$change}'
    ;;
  cloudformation:describe-change-set)
    jq -n --arg change "$change" --arg stack "$stack" --arg run "$run" \
      --arg action "${MOCK_ACTION:-Add}" --slurpfile parameters "$FIXTURE/parameters-$region.json" '
      {ChangeSetId:$change,StackId:$stack,Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",
        ChangeSetName:($run+"-foundation"),Capabilities:["CAPABILITY_IAM"],Parameters:$parameters[0],
        Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
        Changes:[{Type:"Resource",ResourceChange:{Action:$action,LogicalResourceId:"InstallationVolume",ResourceType:"AWS::EC2::Volume"}}]}'
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
    if [[ $(flags --stack-name) == "$run" ]]; then
      printf 'An error occurred (ValidationError): Stack with id %s does not exist\n' "$run" >&2
      exit 1
    fi
    jq -n --arg stack "$stack" --arg run "$run" --arg role "$SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN" '
      {Stacks:[{StackId:$stack,StackName:$run,StackStatus:"CREATE_COMPLETE",EnableTerminationProtection:true,RoleARN:$role,
        Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$run},{Key:"shiftbox_qualification_run",Value:$run}],
        Outputs:[{OutputKey:"Phase",OutputValue:"foundation"},{OutputKey:"InstallationVolumeId",OutputValue:"vol-00000000000000001"}]}]}'
    ;;
  cloudformation:list-stack-resources)
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
[[ $(grep -c 'create-change-set' "$fixture/aws.log") = 4 ]]
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 3 ]]
[[ $(grep -c '^us-east-2 cloudformation execute-change-set' "$fixture/aws.log") = 1 ]]
[[ $(grep -c 'plan .* -destroy\|plan -destroy' "$fixture/terraform.log") = 4 ]]
jq -e '.status == "automated-foundation-only" and (.regions | length == 4) and
  all(.regions[]; .amiBooted and .hostedChangeSetValidated and .terraformDestroyCompleted) and
  .manualGatesPending == true' "$fixture/evidence/automated.json" >/dev/null

# Staffed qualification needs a fresh Ohio console launch, so delete the Ohio
# review stack instead of executing it under the automation identity.
rm -- "$fixture/evidence/automated.json"
: >"$fixture/aws.log"
SHIFTBOX_QUALIFICATION_STAFFED=true deploy
[[ $(grep -c 'delete-stack' "$fixture/aws.log") = 4 ]]
if grep -q 'execute-change-set' "$fixture/aws.log"; then exit 1; fi
jq -e '.status == "ohio-console-launch-pending" and
  .ohioFoundationDeployed == false and .manualGatesPending == true' "$fixture/evidence/automated.json" >/dev/null
printf 'Qualification deployment fixtures passed.\n'
