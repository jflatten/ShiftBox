#!/usr/bin/env bash
# Create one boundary-constrained AMI role and retire it using private S3 state.
set -euo pipefail
umask 077
export AWS_REGION=us-east-2 AWS_DEFAULT_REGION=us-east-2 AWS_PAGER=''
export AWS_CLI_AUTO_PROMPT=off AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
export TF_IN_AUTOMATION=1 TF_WORKSPACE=default

fail() { printf 'Release AMI phase: %s\n' "$1" >&2; exit 1; }
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=platform/packer/aws/terraform-workflow.sh
source "$here/terraform-workflow.sh"
repo=$(cd "$here/../../.." && pwd)
cd "$repo"
root=platform/terraform/publisher/candidate-phase
operation=${1:-}
phase=${2:-}
account=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
bucket=${SHIFTBOX_RELEASE_STATE_BUCKET:-}
run=${GITHUB_RUN_ID:-}
retry=${GITHUB_RUN_ATTEMPT:-}
tag=${RELEASE_TAG:-}
source=${SOURCE_COMMIT:-}
repo_name=jflatten/ShiftBox

[[ "$account" =~ ^[0-9]{12}$ && "$account" != 524941137496 && "$account" != 888293065132 &&
  "$bucket" == "shiftbox-release-state-$account-us-east-2" ]] || fail 'requires the dedicated publisher account and its state bucket'
[[ ${GITHUB_REPOSITORY:-} == "$repo_name" && ${GITHUB_REPOSITORY_ID:-} == 1457179716 &&
  ${GITHUB_REPOSITORY_OWNER_ID:-} == 11561330 && "$run" =~ ^[1-9][0-9]{0,19}$ &&
  "$retry" =~ ^[1-9][0-9]{0,5}$ && ${RUNNER_TEMP:-} == /* && -d "$RUNNER_TEMP" ]] ||
    fail 'requires the verified GitHub publisher and private runner directory'

case "$operation:$phase:$#" in
  check:build:2|check:copy:2|check:share:2|open:build:2|open:copy:3|open:share:4|close:build:2|close:copy:2|close:share:2)
    [[ ${GITHUB_REF:-} == "refs/tags/$tag" && ${GITHUB_SHA:-} == "$source" &&
      "$source" =~ ^[0-9a-f]{40}$ && "$("$repo/platform/release/publish.sh" check-event)" == rc ]] ||
        fail 'requires the active protected RC workflow'
    caller_arn="arn:aws:sts::$account:assumed-role/shiftbox-publisher-release-network/shiftbox-release-network-$run-$retry"
    ;;
  sweep::1)
    [[ ${GITHUB_REF:-} == refs/heads/main && ${GITHUB_REF_PROTECTED:-} == true &&
      ${GITHUB_WORKFLOW_REF:-} == "$repo_name/.github/workflows/cleanup-release-network.yml@refs/heads/main" &&
      ${GITHUB_SHA:-} == "$(git rev-parse HEAD)" && ( ${GITHUB_EVENT_NAME:-} == schedule ||
        ( ${GITHUB_EVENT_NAME:-} == workflow_dispatch && ${GITHUB_ACTOR:-} == jflatten &&
          ${GITHUB_TRIGGERING_ACTOR:-} == jflatten ) ) ]] || fail 'requires the protected cleanup workflow on main'
    caller_arn="arn:aws:sts::$account:assumed-role/shiftbox-publisher-release-network-cleanup/shiftbox-release-cleanup-$run-$retry"
    ;;
  *) fail 'use check <phase>, open build, open copy <packer>, open share <packer> <copy-stage>, close <phase>, or sweep' ;;
esac

aws sts get-caller-identity |
  jq -e --arg arn "$caller_arn" --arg account "$account" '.Arn==$arn and .Account==$account' >/dev/null ||
    fail 'requires the exact publisher control session'
if [[ "$operation" != sweep ]]; then
  [[ "$(git rev-parse HEAD)" == "$source" && "$("$repo/platform/release/publish.sh" check-source "$tag" "$source")" == "$source" ]] ||
    fail 'the active candidate is not the reviewed source'
fi
aws s3api get-bucket-location --bucket "$bucket" --expected-bucket-owner "$account" |
  jq -e '.LocationConstraint=="us-east-2"' >/dev/null
aws s3api get-public-access-block --bucket "$bucket" --expected-bucket-owner "$account" |
  jq -e '.PublicAccessBlockConfiguration=={BlockPublicAcls:true,IgnorePublicAcls:true,BlockPublicPolicy:true,RestrictPublicBuckets:true}' >/dev/null
scratch=$(mktemp -d "$RUNNER_TEMP/release-phase.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT

set_phase() {
  case "$phase" in
    build) job='Build the private Ohio AMI' ;;
    copy) job='Copy the approved Ohio AMI' ;;
    share) job='Share the four approved AMIs' ;;
    *) fail 'unknown candidate phase' ;;
  esac
  prefix="release-builds/$run/$retry/phases/$phase"
  role="shiftbox-publisher-release-$phase-$(printf %s "$run/$retry" | shasum -a 256 | cut -c1-16)"
  role_arn="arn:aws:iam::$account:role/shiftbox-release/$role"
  boundary="arn:aws:iam::$account:policy/shiftbox-publisher-release-$phase-boundary"
}
read_key() {
  aws s3api get-object --bucket "$bucket" --expected-bucket-owner "$account" --key "$1" "$2" |
    jq -e '.ServerSideEncryption=="AES256"' >/dev/null
}
write_key() {
  aws s3api put-object --bucket "$bucket" --expected-bucket-owner "$account" --key "$1" --body "$2" \
    --if-none-match '*' --server-side-encryption AES256 --content-type application/json |
    jq -e '.ServerSideEncryption=="AES256" and (.VersionId|type=="string" and length>0 and .!="null")' >/dev/null
}
has_key() {
  local listed
  listed=$(aws s3api list-objects-v2 --bucket "$bucket" --expected-bucket-owner "$account" --prefix "$1") ||
    fail 'could not inspect phase state'
  jq -e '.Contents==null or (.Contents|type=="array")' <<<"$listed" >/dev/null || fail 'invalid phase state listing'
  jq -e --arg key "$1" 'any(.Contents[]?;.Key==$key)' <<<"$listed" >/dev/null
}
tf() { terraform -chdir="$scratch/$root" "$@"; }
init_state() {
  rm -rf -- "${scratch:?}/${root:?}"
  git archive "$source" "$root" | tar -xf - -C "$scratch"
  printf 'terraform {\n  backend "s3" {}\n}\n' > "$scratch/$root/remote-state.tf"
  cat > "$scratch/backend.hcl" <<BACKEND
bucket               = "$bucket"
key                  = "$prefix/terraform.tfstate"
region               = "us-east-2"
workspace_key_prefix = "release-builds"
allowed_account_ids  = ["$account"]
encrypt              = true
use_lockfile         = true
BACKEND
  tf_logged init -input=false -lockfile=readonly -backend-config="$scratch/backend.hcl" ||
    fail 'could not initialize private phase state'
}
verify_role() {
  jq -e --arg arn "$role_arn" --arg boundary "$boundary" --arg run "$run" --arg retry "$retry" \
    --arg tag "$tag" --arg phase "$phase" --slurpfile input "$scratch/phase.json" '
    .Role |
    (.Tags|map({key:.Key,value:.Value})|from_entries) as $tags |
    .Arn==$arn and .PermissionsBoundary.PermissionsBoundaryArn==$boundary and
    $tags.project=="shiftbox" and $tags.cluster_name=="shiftbox-publisher" and
    $tags.release_run_id==$run and $tags.release_run_attempt==$retry and
    $tags.release_phase==$phase and $tags.environment==("release-"+$tag) and
    (.AssumeRolePolicyDocument.Statement|length==1 and all(.[];
      .Effect=="Allow" and .Action=="sts:AssumeRoleWithWebIdentity" and
      .Condition.StringEquals["sts:RoleSessionName"]==("shiftbox-ami-"+$phase+"-"+$run+"-"+$retry) and
      .Condition.StringEquals["token.actions.githubusercontent.com:ref"]==("refs/tags/"+$tag) and
      .Condition.DateLessThan["aws:CurrentTime"]==$input[0].execution.expires_at))
  ' "$scratch/role.json" >/dev/null
}
lookup_role() {
  if aws iam get-role --role-name "$role" > "$scratch/role.json" 2> "$scratch/role.err"; then
    return 0
  fi
  if ! grep -q NoSuchEntity "$scratch/role.err"; then
    jq -Rr --arg tool AWS -f "$here/workflow-log.jq" "$scratch/role.err" >&2
    fail 'could not read the exact role'
  fi
  return 1
}
read_role() {
  lookup_role || return 1
  verify_role || fail 'live role is outside the exact phase'
}
require_fresh_phase() {
  if has_key "$prefix/phase.json" || has_key "$prefix/terraform.tfstate" ||
    has_key "$prefix/terraform.tfstate.tflock" || lookup_role; then
    fail 'phase lease, state, lock, or role already exists'
  fi
}
verify_journal() {
  jq -e --arg account "$account" --arg phase "$phase" --arg run "$run" --arg retry "$retry" \
    --arg tag "$tag" --arg source "$source" '
    .publisher_account_id==$account and .phase==$phase and .candidate_tag==$tag and
    .execution.run_id==$run and .execution.run_attempt==$retry and .execution.source_commit==$source and
    (.execution.expires_at | fromdateiso8601>0)
  ' "$scratch/phase.json" >/dev/null
}
open_phase() {
  local expires ami snapshot image='' pins='{}' build='null'
  expires=$(jq -nr 'now+3900|todateiso8601')
  if [[ "$phase" == build ]]; then
    local handoff="$RUNNER_TEMP/shiftbox-publisher-network"
    jq -e --arg run "$run" --arg retry "$retry" --arg tag "$tag" --arg source "$source" '
      .run_id==$run and .run_attempt==$retry and .candidate_tag==$tag and .source_commit==$source and
      (.expires_at|fromdateiso8601)-now>=3000
    ' "$handoff/lease.json" >/dev/null
    ami=$(jq -er .source_ami "$handoff/lease.json")
    snapshot=$(jq -er .source_snapshot "$handoff/lease.json")
    expires=$(jq -er .expires_at "$handoff/lease.json")
    build=$(jq -ce --arg snapshot "$snapshot" --arg ami "$ami" --slurpfile lease "$handoff/lease.json" '
      select(.attempt_id==$lease[0].attempt_id and .deadline==$lease[0].expires_at and .source_ami==$ami) |
      {attempt_id,vpc_id,subnet_id:.private_subnet_id,subnet_cidr:.private_subnet_cidr,security_group:.builder_group_id,
       endpoint_id,source_snapshot:$snapshot}
    ' "$handoff/network.json")
  else
    "$repo/platform/release/amis.sh" packer-handoff "$tag" "$source" "$account" "$1" > "$scratch/packer.json"
    image=$(jq -er '.builds[0].artifact_id|select(startswith("us-east-2:"))|split(":")[1]' "$scratch/packer.json")
    ami=$(jq -er '.builds[0].custom_data.sourceAmiId' "$scratch/packer.json")
    if [[ "$phase" == share ]]; then
      pins=$(jq -ce --arg source "$source" --arg tag "$tag" --arg ami "$ami" '
        select(keys==["us-east-1","us-east-2","us-west-1","us-west-2"] and all(.[];.sourceCommit==$source and
          .guestRelease==$tag and .sourceAmiId==$ami and .sourceRegion=="us-east-2")) | map_values(.amiId)
      ' "$2/workspace-images.json")
    fi
  fi
  jq -n --arg account "$account" --arg phase "$phase" --arg tag "$tag" --arg run "$run" --arg retry "$retry" \
    --arg expires "$expires" --arg source "$source" --arg ami "$ami" --arg image "$image" --argjson pins "$pins" --argjson build "$build" '
    {publisher_account_id:$account,phase:$phase,candidate_tag:$tag,
     execution:{run_id:$run,run_attempt:$retry,expires_at:$expires,source_commit:$source,source_ami:$ami},
     built_ami_id:(if $image=="" then null else $image end),regional_amis:$pins,build:$build}
  ' > "$scratch/phase.json"
  require_fresh_phase
  write_key "$prefix/phase.json" "$scratch/phase.json" || fail 'could not reserve a new phase lease'
  init_state
  tf_logged plan -input=false -lock-timeout=2m -var-file="$scratch/phase.json" -out="$scratch/phase.tfplan"
  tf show -json "$scratch/phase.tfplan" | jq -e --arg role "$role" --arg boundary "$boundary" '
    [.resource_changes[]|{address,actions:.change.actions}]==
     [{address:"aws_iam_role.ami_candidate",actions:["create"]},{address:"aws_iam_role_policy.ami_candidate",actions:["create"]}] and
    (.resource_changes[0].change.after|.name==$role and .path=="/shiftbox-release/" and .permissions_boundary==$boundary)
  ' >/dev/null || fail 'unexpected phase plan'
  tf_logged apply -input=false "$scratch/phase.tfplan"
  read_role || fail 'phase role is absent after apply'
  test "$(tf output -raw ami_candidate_role_arn)" = "$role_arn"
  printf '%s\n' "$role_arn"
}
close_phase() {
  if ! has_key "$prefix/phase.json"; then
    if has_key "$prefix/terraform.tfstate" || has_key "$prefix/terraform.tfstate.tflock" || lookup_role; then
      fail 'an existing role, state, or lock has no lease'
    fi
    return 0
  fi
  read_key "$prefix/phase.json" "$scratch/phase.json"
  verify_journal
  init_state
  if [[ "$operation" == sweep ]] && has_key "$prefix/terraform.tfstate.tflock"; then
    read_key "$prefix/terraform.tfstate.tflock" "$scratch/lock.json"
    lock_id=$(jq -er --arg path "$bucket/$prefix/terraform.tfstate" --slurpfile lease "$scratch/phase.json" '
      def epoch: sub("[.][0-9]+Z$";"Z")|fromdateiso8601;
      ($lease[0].execution.expires_at|epoch) as $expires | (.Created|epoch) as $created |
      select(.Path==$path and $expires<=now and ($created<$expires or $created<=now-1800) and
        (.Operation|IN("OperationTypePlan","OperationTypeApply"))) |
      .ID|select(test("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$"))
    ' "$scratch/lock.json")
    tf_logged force-unlock -force "$lock_id"
  fi
  if read_role; then
    # An interrupted provider response can leave a named role outside state.
    : > "$scratch/addresses.txt"
    if has_key "$prefix/terraform.tfstate"; then tf state list > "$scratch/addresses.txt"; fi
    if ! grep -Fxq aws_iam_role.ami_candidate "$scratch/addresses.txt"; then
      tf_logged import -input=false -var-file="$scratch/phase.json" aws_iam_role.ami_candidate "$role"
    fi
    aws iam list-role-policies --role-name "$role" |
      jq -e '.PolicyNames|length<=1 and all(.[];.=="CandidateWorkspaceAMI")' > "$scratch/policy-check.txt"
    tf state list > "$scratch/addresses.txt"
    if ! grep -Fxq aws_iam_role_policy.ami_candidate "$scratch/addresses.txt" &&
      aws iam list-role-policies --role-name "$role" --query 'length(PolicyNames)' --output text | grep -qx 1; then
      tf_logged import -input=false -var-file="$scratch/phase.json" aws_iam_role_policy.ami_candidate "$role:CandidateWorkspaceAMI"
    fi
  fi
  tf_logged plan -destroy -input=false -lock-timeout=2m -var-file="$scratch/phase.json" -out="$scratch/retire.tfplan"
  tf show -json "$scratch/retire.tfplan" | jq -e --arg role "$role" --arg arn "$role_arn" '
    all(.resource_changes[]?;
      .change.actions==["delete"] and
      ((.address=="aws_iam_role.ami_candidate" and .change.before.arn==$arn) or
       (.address=="aws_iam_role_policy.ami_candidate" and .change.before.role==$role and .change.before.name=="CandidateWorkspaceAMI")))
  ' >/dev/null || fail 'unexpected resource in phase teardown'
  tf_logged apply -input=false "$scratch/retire.tfplan"
  if read_role; then fail 'phase role still exists'; fi
  jq '{publisher_account_id,phase,candidate_tag,execution}' "$scratch/phase.json" > "$scratch/retired.json"
  if ! has_key "$prefix/phase-retired.json"; then write_key "$prefix/phase-retired.json" "$scratch/retired.json"; fi
}
sweep_phases() {
  local keys key ref expected
  keys=$(aws s3api list-objects-v2 --bucket "$bucket" --expected-bucket-owner "$account" --prefix release-builds/ |
    jq -r '.Contents[]?.Key') || fail 'could not discover phase leases'
  while IFS= read -r key; do
    [[ "$key" =~ ^release-builds/([1-9][0-9]{0,19})/([1-9][0-9]{0,5})/phases/(build|copy|share)/phase\.json$ ]] || continue
    run=${BASH_REMATCH[1]} retry=${BASH_REMATCH[2]} phase=${BASH_REMATCH[3]}
    set_phase
    read_key "$key" "$scratch/phase.json"
    if has_key "$prefix/phase-retired.json"; then
      read_key "$prefix/phase-retired.json" "$scratch/retired.json"
      jq -e --slurpfile lease "$scratch/phase.json" '.==($lease[0]|{publisher_account_id,phase,candidate_tag,execution})' \
        "$scratch/retired.json" >/dev/null
      continue
    fi
    tag=$(jq -er '.candidate_tag|select(length<=96 and test("^v(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)-rc[.][1-9][0-9]*$"))' "$scratch/phase.json")
    source=$(jq -er '.execution.source_commit|select(test("^[a-f0-9]{40}$"))' "$scratch/phase.json")
    verify_journal
    jq -e '(.execution.expires_at|fromdateiso8601)<=now' "$scratch/phase.json" >/dev/null || continue
    git merge-base --is-ancestor "$source" "$GITHUB_SHA" || fail 'phase source is outside reviewed main'
    ref="repos/$repo_name/actions/runs/$run/attempts/$retry"
    gh api "$ref" | jq -e --arg tag "$tag" --arg source "$source" --arg run "$run" --arg retry "$retry" '
      (.id|tostring)==$run and (.run_attempt|tostring)==$retry and .event=="push" and .name=="Public AWS release" and
      .head_branch==$tag and .head_sha==$source and .repository.id==1457179716 and .actor.id==11561330 and .triggering_actor.id==11561330
    ' >/dev/null
    expected=$(gh api --paginate --slurp "$ref/jobs?per_page=100" | jq -ce       --arg job "$job" --arg run "$run" --arg retry "$retry" --arg source "$source" '
      [.[]|.jobs[]|select(.name==$job)]|select(length==1)|.[0]|
      select((.run_id|tostring)==$run and (.run_attempt|tostring)==$retry and .head_sha==$source)')
    jq -e '.status=="completed" and .completed_at!=null' <<<"$expected" >/dev/null || continue
    close_phase
  done <<<"$keys"
}

case "$operation" in
  check) set_phase; require_fresh_phase; printf 'Verified the %s phase role and state are absent.\n' "$phase" ;;
  open) set_phase; open_phase "${@:3}" ;;
  close) set_phase; close_phase ;;
  sweep) sweep_phases ;;
esac
