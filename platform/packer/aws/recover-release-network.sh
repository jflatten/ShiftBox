#!/usr/bin/env bash
# Retire an expired publisher build only after GitHub confirms its job ended.
# shellcheck disable=SC2016
set -euo pipefail
umask 077

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=jflatten/ShiftBox
workflow="$repo/.github/workflows/cleanup-release-network.yml@refs/heads/main"
publisher=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
bucket=${SHIFTBOX_RELEASE_STATE_BUCKET:-}
region=us-east-2
lease_pattern='^release-builds/(rehearsals/)?[1-9][0-9]{0,19}/[1-9][0-9]{0,5}/lease[.]json\z'
candidate_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*\z'
export AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true AWS_PAGER='' AWS_CLI_AUTO_PROMPT=off

die() {
  printf 'Publisher network recovery: %s\n' "$*" >&2
  exit 1
}

aws_read() {
  aws --region "$region" "$@" --output json
}

read_object() {
  aws --region "$region" s3api get-object --bucket "$bucket" --expected-bucket-owner "$publisher" \
    --key "$1" --output json "$2" |
    jq -se 'length==1 and .[0].ServerSideEncryption=="AES256"' >/dev/null
}

recover() {
  local key=$1 run=$2 attempt=$3 lease="$scratch/lease.json" marker="$scratch/retired.json"
  local marker_key="${key%/lease.json}/retired.json" tag source api job attempt_id
  local rehearsal=false job_name='Build the private Ohio AMI' run_name='Public AWS release'
  local event=push path=.github/workflows/release.yml head_branch ref
  attempt_id=$(printf '%s' "$run/$attempt" | shasum -a 256 | cut -c1-14) || return
  if [[ "$key" == release-builds/rehearsals/* ]]; then
    [[ "$attempt" == 1 ]] || return 1
    rehearsal=true
    job_name='Rehearse the private Ohio network'
    run_name='Rehearse publisher build network'
    event=workflow_dispatch
    path=.github/workflows/rehearse-release-network.yml
    attempt_id="r${attempt_id:0:13}"
  fi
  read_object "$key" "$lease" || return
  jq -se --arg run "$run" --arg attempt "$attempt" --arg publisher "$publisher" --arg id "rel-$attempt_id" \
    --arg job "$job_name" --arg candidate "$candidate_pattern" '
    length==1 and (.[0] | type=="object" and .schema==1 and .run_id==$run and .run_attempt==$attempt and
    .publisher_account_id==$publisher and .job_name==$job and
    .attempt_id==$id and
    (.candidate_tag|strings|length<=64 and test($candidate)) and
    (.source_commit|strings|test("^[0-9a-f]{40}\\z")) and
    (.expires_at|strings|try (fromdateiso8601|type=="number") catch false))
  ' "$lease" >/dev/null || return
  tag=$(jq -er '.candidate_tag' "$lease") || return
  if [[ "$rehearsal" == true ]]; then
    [[ "$tag" == v0.0.0-rc.1 ]] || return 1
    head_branch=main ref=refs/heads/main
  else
    head_branch=$tag ref="refs/tags/$tag"
  fi
  if jq -e --arg key "$marker_key" 'index($key)!=null' <<<"$keys" >/dev/null; then
    read_object "$marker_key" "$marker" || return
    jq -se --slurpfile lease "$lease" '
      length==1 and .[0]==($lease[0]|{schema,run_id,run_attempt,publisher_account_id,candidate_tag,source_commit,attempt_id})
    ' "$marker" >/dev/null
    return
  fi
  # The normal job owns cleanup during the lease, even if GitHub status lags.
  jq -e '(.expires_at|fromdateiso8601)<=now' "$lease" >/dev/null || return 0

  source=$(jq -er '.source_commit' "$lease") || return
  git merge-base --is-ancestor "$source" "$GITHUB_SHA" || return
  api="repos/$repo/actions/runs/$run/attempts/$attempt"
  gh api "$api" |
    jq -e --arg run "$run" --arg attempt "$attempt" --arg source "$source" \
      --arg name "$run_name" --arg event "$event" --arg branch "$head_branch" --arg path "$path" --arg ref "$ref" '
      (.id|tostring)==$run and (.run_attempt|tostring)==$attempt and
      .name==$name and .event==$event and .head_branch==$branch and .head_sha==$source and
      (.path==$path or .path==($path+"@"+$ref)) and
      .actor.id==11561330 and .triggering_actor.id==11561330 and
      .repository.id==1457179716 and .head_repository.id==1457179716
    ' >/dev/null || return
  job=$(gh api --paginate --slurp "$api/jobs?per_page=100" |
    jq -ce --arg name "$job_name" --arg run "$run" --arg attempt "$attempt" --arg source "$source" '
      [.[]|.jobs[]|select(.name==$name)] | select(length==1) | .[0] |
      select((.run_id|tostring)==$run and (.run_attempt|tostring)==$attempt and .head_sha==$source)
    ') || return
  # A release waiting for copy or share is safe once its network-owning build
  # job has ended. A pending or running build can still own the same state lock.
  case "$(jq -r '.status' <<<"$job")" in
    queued|waiting|pending|requested|in_progress) return 0 ;;
    completed) ;;
    *) return 1 ;;
  esac
  jq -e --slurpfile lease "$lease" '
    (.conclusion|IN("success","failure","cancelled","timed_out","neutral","stale","action_required","startup_failure")) and
    (.started_at|fromdateiso8601) <= ($lease[0].expires_at|fromdateiso8601) and
    (.completed_at|fromdateiso8601) >= (.started_at|fromdateiso8601) and
    (.completed_at|fromdateiso8601) <= now
  ' <<<"$job" >/dev/null || return

  SHIFTBOX_RELEASE_RECOVERY=true SHIFTBOX_RELEASE_NETWORK_REHEARSAL="$rehearsal" SOURCE_COMMIT="$source" RELEASE_TAG="$tag" \
    bash "$here/release-network.sh" retire "$run" "$attempt"
}

[[ $# == 0 ]] || die 'usage: recover-release-network.sh'
[[ ${GITHUB_REPOSITORY:-} == "$repo" && ${GITHUB_REPOSITORY_ID:-} == 1457179716 &&
  ${GITHUB_REPOSITORY_OWNER_ID:-} == 11561330 && ${GITHUB_REF:-} == refs/heads/main &&
  ${GITHUB_REF_PROTECTED:-} == true && ${GITHUB_WORKFLOW_REF:-} == "$workflow" &&
  ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ && ${GITHUB_RUN_ID:-} =~ ^[1-9][0-9]{0,19}$ &&
  ${GITHUB_RUN_ATTEMPT:-} =~ ^[1-9][0-9]{0,5}$ && $(git rev-parse HEAD) == "$GITHUB_SHA" ]] ||
  die 'requires the checked-out protected cleanup workflow on main'
if [[ ${GITHUB_EVENT_NAME:-} != schedule ]]; then
  [[ ${GITHUB_EVENT_NAME:-} == workflow_dispatch && ${GITHUB_ACTOR:-} == jflatten &&
    ${GITHUB_ACTOR_ID:-} == 11561330 && ${GITHUB_TRIGGERING_ACTOR:-} == jflatten ]] ||
    die 'requires the scheduled sweep or a dispatch by the publisher maintainer'
fi
[[ $publisher =~ ^[0-9]{12}$ && $publisher != 524941137496 && $publisher != 888293065132 &&
  $bucket == "shiftbox-release-state-$publisher-$region" ]] ||
  die 'requires the dedicated publisher account and its release state bucket'
caller=$(aws_read sts get-caller-identity) || die 'AWS caller identity could not be verified'
caller_arn="arn:aws:sts::$publisher:assumed-role/shiftbox-publisher-release-network-cleanup/shiftbox-release-cleanup-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
jq -e --arg account "$publisher" --arg arn "$caller_arn" '.Account==$account and .Arn==$arn' <<<"$caller" >/dev/null ||
  die 'requires the cleanup role session for this workflow attempt'

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
keys=$(aws_read s3api list-objects-v2 --bucket "$bucket" --expected-bucket-owner "$publisher" \
  --prefix release-builds/ |
  jq -ces '
    select(length==1) | (.[0].Contents // []) |
    select(type=="array" and all(.[];(.Key|type)=="string")) | map(.Key)
  ') ||
  die 'could not list publisher build journals'
result=0
if ! jq -e --arg pattern "$lease_pattern" '
  [.[]|select(endswith("/lease.json"))] | all(.[];test($pattern))
' <<<"$keys" >/dev/null; then
  printf 'Publisher network recovery: unexpected journal key in the release prefix.\n' >&2
  result=1
fi
leases=$(jq -r --arg pattern "$lease_pattern" '.[]|select(test($pattern))' <<<"$keys")
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  attempt_path=${key%/lease.json}
  attempt=${attempt_path##*/}
  run_path=${attempt_path%/*}
  run=${run_path##*/}
  if ! recover "$key" "$run" "$attempt"; then
    printf 'Publisher network recovery: could not verify or retire run %s attempt %s.\n' "$run" "$attempt" >&2
    result=1
  fi
done <<<"$leases"
exit "$result"
