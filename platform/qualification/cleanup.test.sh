#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
export CLEANUP_FIXTURE=$tmp
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
cat >"$tmp/bin/aws" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
region=$2 service=$3 action=$4
shift 4
stack=sbq-123456789-1
account=111122223333
arn="arn:aws:cloudformation:$region:$account:stack/$stack/1234-abcd"
volume="arn:aws:ec2:$region:$account:volume/vol-1234567890abcdef0"
secret="arn:aws:secretsmanager:$region:$account:secret:$stack-PostgresAdminSecret-aabbccddeeff"
if [[ $region == us-east-2 && ${CASE:-none} =~ ^(owned|foreign|stale-dns|restore|foreign-restore)$ &&
  ! -e "$CLEANUP_FIXTURE/stack-deleted" ]]; then
  present=true
else
  present=false
fi
tags=$(jq -cn --arg name "$stack" '[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:$name},{Key:"shiftbox_qualification_run",Value:$name}]')
probe=false
if [[ $region == us-west-2 && ${CASE:-none} =~ ^(probe|default-vpc)$ && ( $* == *"$stack-p"* || $* != *"$stack"* ) ]]; then
  probe=true
  tags=$(jq -c --arg name "$stack-p" 'map(if .Key == "cluster_name" then .Value = $name else . end)' <<<"$tags")
fi
case "$service:$action" in
  sts:get-caller-identity)
    printf '%s\n' "${ACCOUNT:-$account}"
    ;;
  cloudformation:list-stacks)
    if "$present"; then
      jq -cn --arg name "$stack" --arg id "$arn" '{StackSummaries:[{StackName:$name,StackId:$id,StackStatus:"CREATE_COMPLETE"}]}'
    else
      printf '{"StackSummaries":[]}\n'
    fi
    ;;
  cloudformation:describe-stacks)
    if [[ ${CASE:-none} == foreign ]]; then tags='[]'; fi
    jq -cn --arg name "$stack" --arg id "$arn" --argjson tags "$tags" '{Stacks:[{StackName:$name,StackId:$id,StackStatus:"CREATE_COMPLETE",EnableTerminationProtection:true,Tags:$tags}]}'
    ;;
  cloudformation:list-stack-resources)
    jq -cn --arg dnsState "$([[ ${CASE:-none} == stale-dns ]] && echo DELETE_SKIPPED || echo DELETE_COMPLETE)" '
      {StackResourceSummaries:[
        {ResourceType:"AWS::EC2::VPC",LogicalResourceId:"Vpc",PhysicalResourceId:"vpc-abcd",ResourceStatus:"DELETE_COMPLETE"},
        {ResourceType:"AWS::EC2::NatGateway",PhysicalResourceId:"nat-abcd",ResourceStatus:"DELETE_COMPLETE"},
        {ResourceType:"AWS::Route53::HostedZone",PhysicalResourceId:"Z123ABC",ResourceStatus:$dnsState},
        {ResourceType:"AWS::EC2::Volume",LogicalResourceId:"InstallationVolume",PhysicalResourceId:"vol-1234",ResourceStatus:"DELETE_SKIPPED"}
      ]}' ;;
  resourcegroupstaggingapi:get-resources)
    if [[ $region == us-east-2 && ${CASE:-none} == owned && $* != *"-p"* ]]; then
      jq -cn --arg volume "$volume" --arg secret "$secret" --argjson tags "$tags" \
        --argjson volumeGone "$([[ -e "$CLEANUP_FIXTURE/volume-deleted" ]] && echo true || echo false)" \
        --argjson secretGone "$([[ -e "$CLEANUP_FIXTURE/secret-deleted" ]] && echo true || echo false)" '
          {ResourceTagMappingList:[(if $volumeGone then empty else {ResourceARN:$volume,Tags:$tags} end),
            (if $secretGone then empty else {ResourceARN:$secret,Tags:$tags} end)]}'
    elif "$probe" && [[ ! -e "$CLEANUP_FIXTURE/vpc-deleted" ]]; then
      jq -cn --arg arn "arn:aws:ec2:$region:$account:vpc/vpc-abcd" --argjson tags "$tags" '
        {ResourceTagMappingList:[{ResourceARN:$arn,Tags:$tags}]}'
    else
      printf '{"ResourceTagMappingList":[]}\n'
    fi
    ;;
  ec2:describe-instances)
    if "$probe" && [[ ! -e "$CLEANUP_FIXTURE/instance-terminated" ]]; then
      jq -cn --arg account "$account" --argjson tags "$tags" '
        {Reservations:[{OwnerId:$account,Instances:[{InstanceId:"i-abcd",VpcId:"vpc-abcd",State:{Name:"running"},Tags:$tags}]}]}'
    elif "$present" && [[ ${CASE:-none} =~ ^(restore|foreign-restore)$ && $* == *"$stack"* && $* != *"$stack-p"* ]]; then
      jq -cn --arg account "$account" --argjson tags "$tags" \
        --arg vpc "$([[ ${CASE:-none} == foreign-restore ]] && echo vpc-dcba || echo vpc-abcd)" \
        --argjson restoreGone "$([[ -e "$CLEANUP_FIXTURE/instance-terminated" ]] && echo true || echo false)" '
          {Reservations:[{OwnerId:$account,Instances:[
            {InstanceId:"i-dddd",VpcId:"vpc-abcd",State:{Name:"running"},Tags:$tags},
            (if $restoreGone then empty else {InstanceId:"i-abcd",VpcId:$vpc,State:{Name:"running"},
              Tags:($tags + [{Key:"shiftbox_qualification_component",Value:"restore"}])} end)]}]}'
    else
      printf '{"Reservations":[]}\n'
    fi ;;
  ec2:describe-vpcs)
    jq -cn --arg account "$account" --argjson tags "$tags" \
      --argjson isDefault "$([[ ${CASE:-none} == default-vpc ]] && echo true || echo false)" '
      {Vpcs:[{VpcId:"vpc-abcd",OwnerId:$account,IsDefault:$isDefault,Tags:$tags}]}'
    ;;
  ec2:describe-nat-gateways)
    if [[ ${CASE:-none} == unindexed-nat && $region == us-east-2 && $* == *"$stack"* && $* != *"$stack-p"* ]]; then
      printf '{"NatGateways":[{"NatGatewayId":"nat-abcd","State":"available"}]}\n'
    else
      printf '{"NatGateways":[]}\n'
    fi ;;
  ec2:describe-volumes)
    jq -cn --argjson tags "$tags" '{Volumes:[{VolumeId:"vol-1234567890abcdef0",State:"available",Attachments:[],Tags:$tags}]}'
    ;;
  secretsmanager:describe-secret)
    jq -cn --arg secret "$secret" --argjson tags "$tags" '{ARN:$secret,Tags:$tags}'
    ;;
  cloudformation:update-termination-protection|cloudformation:delete-stack|ec2:delete-volume|secretsmanager:delete-secret|ec2:terminate-instances|ec2:delete-vpc)
    printf '%s:%s %s %s\n' "$service" "$action" "$region" "$*" >>"$CLEANUP_FIXTURE/writes"
    [[ $action != delete-stack ]] || touch "$CLEANUP_FIXTURE/stack-deleted"
    [[ $action != delete-volume ]] || touch "$CLEANUP_FIXTURE/volume-deleted"
    [[ $action != delete-secret ]] || touch "$CLEANUP_FIXTURE/secret-deleted"
    [[ $action != terminate-instances ]] || touch "$CLEANUP_FIXTURE/instance-terminated"
    [[ $action != delete-vpc ]] || touch "$CLEANUP_FIXTURE/vpc-deleted"
    ;;
  *) printf 'unexpected fixture AWS operation %s:%s\n' "$service" "$action" >&2; exit 97 ;;
esac
MOCK
cat >"$tmp/bin/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == api && $2 == repos/jflatten/ShiftBox/actions/runs/123456789* ]]
jq -cn --arg state "${RUN_STATE:-completed}" --arg path "${RUN_PATH:-.github/workflows/qualify-aws.yml@main}" \
  --arg updated "${RUN_UPDATED:-2020-01-01T00:00:00Z}" --arg actor "${RUN_ACTOR:-jflatten}" '
  {id:123456789,run_attempt:1,path:$path,event:"workflow_dispatch",head_branch:"main",
   head_repository:{full_name:"jflatten/ShiftBox"},actor:{login:$actor},triggering_actor:{login:$actor},
   status:$state,conclusion:(if $state=="completed" then "cancelled" else null end),updated_at:$updated}
'
MOCK
cat >"$tmp/bin/sleep" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REF=refs/heads/main
export GITHUB_WORKFLOW_REF=jflatten/ShiftBox/.github/workflows/qualify-aws.yml@refs/heads/main
export GITHUB_RUN_ID=123456789 GITHUB_RUN_ATTEMPT=1 GITHUB_EVENT_NAME=workflow_dispatch
export SHIFTBOX_QUALIFICATION_ACCOUNT_ID=111122223333 SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=444455556666

# Missing and duplicated account pins fail before consulting AWS.
if SHIFTBOX_QUALIFICATION_ACCOUNT_ID='' bash "$here/cleanup.sh" sweep >"$tmp/no-pin" 2>&1; then exit 1; fi
grep -q 'missing or unsafe dedicated testing account' "$tmp/no-pin"
if SHIFTBOX_QUALIFICATION_ACCOUNT_ID=$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID \
    bash "$here/cleanup.sh" sweep >"$tmp/shared-pin" 2>&1; then exit 1; fi
grep -q 'missing or unsafe dedicated testing account' "$tmp/shared-pin"

# Cleanup must reject any AWS account other than the dedicated testing account.
if ACCOUNT=524941137496 bash "$here/cleanup.sh" sweep >"$tmp/wrong-account" 2>&1; then exit 1; fi
grep -q 'dedicated testing account' "$tmp/wrong-account"
test ! -e "$tmp/writes"

# A running job is never reaped even if its stack has existed for a long time.
CASE=owned RUN_STATE=in_progress bash "$here/cleanup.sh" sweep
test ! -e "$tmp/writes"

# A just-finished attempt is still inside the independent cleanup grace period.
CASE=owned RUN_UPDATED="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" bash "$here/cleanup.sh" sweep
test ! -e "$tmp/writes"

# A different workflow cannot claim an otherwise correctly named stack.
if CASE=owned RUN_PATH=.github/workflows/ci.yml bash "$here/cleanup.sh" sweep >"$tmp/wrong-workflow" 2>&1; then exit 1; fi
grep -q 'qualification run identity' "$tmp/wrong-workflow"
test ! -e "$tmp/writes"

# The workflow run must have been authorized by the approved maintainer.
if CASE=owned RUN_ACTOR=unexpected bash "$here/cleanup.sh" sweep >"$tmp/wrong-actor" 2>&1; then exit 1; fi
grep -q 'qualification run identity' "$tmp/wrong-actor"
test ! -e "$tmp/writes"

# Stack names alone are insufficient authority to disable termination protection.
if CASE=foreign bash "$here/cleanup.sh" sweep >"$tmp/foreign" 2>&1; then exit 1; fi
grep -q 'ownership of qualification stack' "$tmp/foreign"
test ! -e "$tmp/writes"

# A previous attempt's resources stop a new run before it makes any changes.
if CASE=owned bash "$here/cleanup.sh" empty >"$tmp/occupied" 2>&1; then exit 1; fi
grep -q 'qualification resources remain' "$tmp/occupied"
test ! -e "$tmp/writes"

# A cancelled, aged-out run is reaped even when the original runner is gone.
CASE=owned bash "$here/cleanup.sh" sweep
grep -q '^cloudformation:update-termination-protection us-east-2 ' "$tmp/writes"
grep -q '^cloudformation:delete-stack us-east-2 ' "$tmp/writes"
grep -q '^ec2:delete-volume us-east-2 --volume-id vol-1234567890abcdef0' "$tmp/writes"
grep -q '^secretsmanager:delete-secret us-east-2 --secret-id .*:secret:sbq-123456789-1-PostgresAdminSecret-.* --force-delete-without-recovery ' "$tmp/writes"
test "$(wc -l <"$tmp/writes" | tr -d ' ')" = 4

# Reconciliation is idempotent after deletion has settled.
CASE=owned bash "$here/cleanup.sh" sweep
test "$(wc -l <"$tmp/writes" | tr -d ' ')" = 4

# Even an apparently valid probe tag cannot cause default VPC termination.
if CASE=default-vpc bash "$here/cleanup.sh" sweep >"$tmp/default-vpc" 2>&1; then exit 1; fi
grep -q 'unverified or default VPC' "$tmp/default-vpc"
test "$(wc -l <"$tmp/writes" | tr -d ' ')" = 4

# After losing Terraform state, a probe can still be torn down from tags.
CASE=probe bash "$here/cleanup.sh" sweep
grep -q '^ec2:terminate-instances us-west-2 --instance-ids i-abcd ' "$tmp/writes"
grep -q '^ec2:delete-vpc us-west-2 --vpc-id vpc-abcd ' "$tmp/writes"
test "$(wc -l <"$tmp/writes" | tr -d ' ')" = 6

# A stack marked deleted must still prove removal of its regular DNS resources.
rm "$tmp/stack-deleted"
if CASE=stale-dns bash "$here/cleanup.sh" sweep >"$tmp/stale-dns" 2>&1; then exit 1; fi
grep -q 'regular AWS stack resource deletion' "$tmp/stale-dns"
if grep -q 'Z123ABC' "$tmp/stale-dns"; then exit 1; fi

# Verify the native EC2 NAT list even when the tag index returns no resources.
if CASE=unindexed-nat RUN_STATE=in_progress bash "$here/cleanup.sh" run >"$tmp/nat" 2>&1; then exit 1; fi
grep -q 'tagged NAT gateway remains' "$tmp/nat"
if grep -q 'nat-abcd' "$tmp/nat"; then exit 1; fi

# A restore marker is insufficient when the target is outside the stack VPC.
rm "$tmp/stack-deleted" "$tmp/instance-terminated"
if CASE=foreign-restore RUN_STATE=in_progress bash "$here/cleanup.sh" run >"$tmp/foreign-restore" 2>&1; then exit 1; fi
grep -q 'does not belong to the qualification VPC' "$tmp/foreign-restore"
if grep -q '^ec2:terminate-instances us-east-2' "$tmp/writes"; then exit 1; fi

# Drain only the exact-VPC PostgreSQL restore target before CloudFormation.
CASE=restore RUN_STATE=in_progress bash "$here/cleanup.sh" run
grep -q '^ec2:terminate-instances us-east-2 --instance-ids i-abcd ' "$tmp/writes"
if grep -q '^ec2:terminate-instances us-east-2 .* i-dddd ' "$tmp/writes"; then exit 1; fi

printf 'Qualification cleanup fixtures passed.\n'
