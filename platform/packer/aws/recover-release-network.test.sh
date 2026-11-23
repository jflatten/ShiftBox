#!/usr/bin/env bash
set -euo pipefail
umask 077

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/bin"
cp "$here/recover-release-network.sh" "$scratch/"
export PATH="$scratch/bin:$PATH"
export FIXTURE_DIR="$scratch" RECOVERY_TRACE="$scratch/retire.log"
export GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REPOSITORY_ID=1457179716 GITHUB_REPOSITORY_OWNER_ID=11561330
export GITHUB_REF=refs/heads/main GITHUB_REF_PROTECTED=true GITHUB_EVENT_NAME=schedule
export GITHUB_WORKFLOW_REF=jflatten/ShiftBox/.github/workflows/cleanup-release-network.yml@refs/heads/main
export GITHUB_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa GITHUB_RUN_ID=30012345678 GITHUB_RUN_ATTEMPT=1
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export SHIFTBOX_RELEASE_STATE_BUCKET=shiftbox-release-state-111122223333-us-east-2
export FIXTURE_SOURCE=0123456789abcdef0123456789abcdef01234567 FIXTURE_TAG=v1.0.0-rc.1
export FIXTURE_RUN=20012345678 FIXTURE_ATTEMPT=2
export FIXTURE_EXPIRES=2020-01-01T00:01:00Z

lease_base() {
  if [[ ${FIXTURE_REHEARSAL:-} == true ]]; then
    printf 'release-builds/rehearsals/%s/%s\n' "$FIXTURE_RUN" "$FIXTURE_ATTEMPT"
  else
    printf 'release-builds/%s/%s\n' "$FIXTURE_RUN" "$FIXTURE_ATTEMPT"
  fi
}
export -f lease_base

cat >"$scratch/bin/aws" <<'AWS'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == --region && $2 == us-east-2 && ${AWS_IGNORE_CONFIGURED_ENDPOINT_URLS:-} == true ]]
shift 2
case "$1 $2" in
  "sts get-caller-identity")
    account=${FIXTURE_ACCOUNT:-111122223333}
    role=${FIXTURE_ROLE:-shiftbox-publisher-release-network-cleanup}
    jq -n --arg account "$account" \
      --arg arn "arn:aws:sts::$account:assumed-role/$role/shiftbox-release-cleanup-30012345678-1" \
      '{Account:$account,Arn:$arn}' ;;
  "s3api list-objects-v2")
    [[ " $* " == *" --expected-bucket-owner 111122223333 "* &&
      " $* " == *" --bucket shiftbox-release-state-111122223333-us-east-2 "* &&
      " $* " == *" --prefix release-builds/ "* ]]
    jq -n --arg base "$(lease_base)" \
      --arg marker "${FIXTURE_MARKER:-}" --arg empty "${FIXTURE_EMPTY:-}" '
      {Contents:([{Key:($base+"/lease.json")},{Key:($base+"/terraform.tfstate")},
        {Key:($base+"/terraform.tfstate.tflock")}] +
        (if $marker=="" then [] else [{Key:($base+"/retired.json")}] end))} |
      if $empty=="true" then del(.Contents) else . end' ;;
  "s3api get-object")
    [[ " $* " == *" --expected-bucket-owner 111122223333 "* &&
      " $* " == *" --bucket shiftbox-release-state-111122223333-us-east-2 "* ]]
    base=$(lease_base)
    attempt_id=${FIXTURE_ATTEMPT_ID:-rel-698990500ca711}
    job="${FIXTURE_JOB:-Build the private Ohio AMI}"
    file="${!#}"
    if [[ " $* " == *" --key $base/lease.json "* ]]; then
      jq -n --arg run "${FIXTURE_LEASE_RUN:-$FIXTURE_RUN}" --arg attempt "$FIXTURE_ATTEMPT" \
        --arg tag "$FIXTURE_TAG" --arg source "$FIXTURE_SOURCE" --arg expires "$FIXTURE_EXPIRES" \
        --arg attempt_id "$attempt_id" --arg job "$job" '{
          schema:1,run_id:$run,run_attempt:$attempt,candidate_tag:$tag,source_commit:$source,
          expires_at:$expires,job_name:$job,attempt_id:$attempt_id,
          publisher_account_id:"111122223333",source_ami:"ami-00000000000000001",variables:{}
        }' >"$file"
    elif [[ " $* " == *" --key $base/retired.json "* ]]; then
      jq -n --arg run "$FIXTURE_RUN" --arg attempt "$FIXTURE_ATTEMPT" \
        --arg tag "$FIXTURE_TAG" --arg source "${FIXTURE_MARKER_SOURCE:-$FIXTURE_SOURCE}" \
        --arg attempt_id "$attempt_id" '{
          schema:1,run_id:$run,run_attempt:$attempt,candidate_tag:$tag,source_commit:$source,
          attempt_id:$attempt_id,publisher_account_id:"111122223333"
        }' >"$file"
    else
      exit 93
    fi
    jq -n --arg encryption "${FIXTURE_ENCRYPTION:-AES256}" '{ServerSideEncryption:$encryption}' ;;
  *) exit 92 ;;
esac
AWS
cat >"$scratch/bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == api ]]
base=repos/jflatten/ShiftBox/actions/runs/$FIXTURE_RUN/attempts/$FIXTURE_ATTEMPT
case "${!#}" in
  "$base")
    [[ ${FIXTURE_GH_UNAVAILABLE:-} != true ]] || exit 1
    jq -n --argjson run "$FIXTURE_RUN" --argjson attempt "${FIXTURE_ACTUAL_ATTEMPT:-$FIXTURE_ATTEMPT}" \
      --arg path "${FIXTURE_WORKFLOW_PATH:-.github/workflows/release.yml}" \
      --arg ref "${FIXTURE_HEAD_BRANCH:-$FIXTURE_TAG}" --arg source "$FIXTURE_SOURCE" \
      --arg name "${FIXTURE_WORKFLOW_NAME:-Public AWS release}" --arg event "${FIXTURE_EVENT:-push}" '
      {id:$run,run_attempt:$attempt,path:$path,head_branch:$ref,head_sha:$source,
       name:$name,event:$event,status:"waiting",actor:{id:11561330},triggering_actor:{id:11561330},
       repository:{id:1457179716},head_repository:{id:1457179716}}' ;;
  "$base/jobs?per_page=100")
    [[ " $* " == *" --paginate "* && " $* " == *" --slurp "* ]]
    jq -n --argjson run "$FIXTURE_RUN" --argjson attempt "$FIXTURE_ATTEMPT" \
      --arg name "${FIXTURE_JOB:-Build the private Ohio AMI}" \
      --arg source "${FIXTURE_JOB_SOURCE:-$FIXTURE_SOURCE}" --arg status "${FIXTURE_STATUS:-completed}" \
      --arg conclusion "${FIXTURE_CONCLUSION:-cancelled}" --arg duplicate "${FIXTURE_DUPLICATE_JOB:-}" '
      [{jobs:([{id:7771234,name:$name,run_id:$run,run_attempt:$attempt,
       head_sha:$source,status:$status,conclusion:$conclusion,started_at:"2020-01-01T00:00:00Z",
       completed_at:"2020-01-01T00:00:30Z"}] |
       if $duplicate!="true" then . else . + . end)}]' ;;
  *) exit 91 ;;
esac
GH
cat >"$scratch/bin/git" <<'GIT'
#!/usr/bin/env bash
set -euo pipefail
if [[ $* == "rev-parse HEAD" ]]; then
  printf '%s\n' "$GITHUB_SHA"
elif [[ $* == "merge-base --is-ancestor $FIXTURE_SOURCE $GITHUB_SHA" ]]; then
  [[ ${FIXTURE_OFF_MAIN:-} != true ]]
else
  exit 90
fi
GIT
cat >"$scratch/release-network.sh" <<'RETIRE'
#!/usr/bin/env bash
set -euo pipefail
[[ $# == 3 && $1 == retire && $2 == "$FIXTURE_RUN" && $3 == "$FIXTURE_ATTEMPT" ]]
[[ ${SHIFTBOX_RELEASE_RECOVERY:-} == true && $SOURCE_COMMIT == "$FIXTURE_SOURCE" && $RELEASE_TAG == "$FIXTURE_TAG" ]]
[[ ${SHIFTBOX_RELEASE_NETWORK_REHEARSAL:-false} == "${FIXTURE_REHEARSAL:-false}" ]]
[[ ${FIXTURE_RETIRE_UNAVAILABLE:-} != true ]] || exit 1
printf '%s %s\n' "$2" "$3" >>"$RECOVERY_TRACE"
RETIRE
chmod +x "$scratch/bin/"*

run() {
  : >"$RECOVERY_TRACE"
  bash "$scratch/recover-release-network.sh"
}

reject() {
  if run >"$scratch/rejection.log" 2>&1; then
    printf 'Unsafe or unverifiable recovery was accepted.\n' >&2
    exit 1
  fi
  test ! -s "$RECOVERY_TRACE"
}

# A cancelled build can be retired even when the release run remains held at a later gate.
run
test "$(cat "$RECOVERY_TRACE")" = "$FIXTURE_RUN $FIXTURE_ATTEMPT"

# Runner loss can end a build with failure and still permits the same scoped retirement.
FIXTURE_CONCLUSION=failure run
test "$(cat "$RECOVERY_TRACE")" = "$FIXTURE_RUN $FIXTURE_ATTEMPT"

# The repository owner can make the same exact recovery outside the scheduled sweep.
GITHUB_EVENT_NAME=workflow_dispatch GITHUB_ACTOR=jflatten GITHUB_ACTOR_ID=11561330 GITHUB_TRIGGERING_ACTOR=jflatten run
test "$(cat "$RECOVERY_TRACE")" = "$FIXTURE_RUN $FIXTURE_ATTEMPT"

# An expired journal never authorizes removal while its original build is active.
FIXTURE_STATUS=in_progress run
test ! -s "$RECOVERY_TRACE"

# The scheduled sweep leaves the lock untouched while the lease is still valid.
FIXTURE_EXPIRES=2099-01-01T00:01:00Z run
test ! -s "$RECOVERY_TRACE"

# A verified matching retirement marker prevents redundant Terraform work on each sweep.
FIXTURE_MARKER=true run
test ! -s "$RECOVERY_TRACE"

# No journals is a normal result for a new publisher backend.
FIXTURE_EMPTY=true run
test ! -s "$RECOVERY_TRACE"

# Only the locked-down main workflow and its read-back AWS role can attempt recovery.
GITHUB_REF_PROTECTED=false reject
GITHUB_REPOSITORY_ID=1 reject
GITHUB_EVENT_NAME=push reject
GITHUB_EVENT_NAME=workflow_dispatch GITHUB_ACTOR=jflatten GITHUB_ACTOR_ID=11561330 GITHUB_TRIGGERING_ACTOR=other reject
FIXTURE_ACCOUNT=524941137496 reject
FIXTURE_ROLE=shiftbox-publisher-release-network reject
SHIFTBOX_RELEASE_STATE_BUCKET=unrelated-bucket reject

# A lease, marker, run or job with conflicting provenance must never reach Terraform.
FIXTURE_LEASE_RUN=20099999999 reject
FIXTURE_ATTEMPT_ID=rel-ffffffffffffff reject
FIXTURE_ENCRYPTION=none reject
FIXTURE_MARKER=true FIXTURE_MARKER_SOURCE=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb reject
FIXTURE_ACTUAL_ATTEMPT=1 reject
FIXTURE_WORKFLOW_PATH=.github/workflows/ci.yml reject
FIXTURE_JOB_SOURCE=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb reject
FIXTURE_DUPLICATE_JOB=true reject
FIXTURE_OFF_MAIN=true reject
FIXTURE_GH_UNAVAILABLE=true reject
FIXTURE_RETIRE_UNAVAILABLE=true reject

# Recovery recognizes only the completed main rehearsal that owns this journal.
export FIXTURE_REHEARSAL=true FIXTURE_ATTEMPT=1 FIXTURE_TAG=v0.0.0-rc.1
export FIXTURE_JOB='Rehearse the private Ohio network' FIXTURE_HEAD_BRANCH=main FIXTURE_EVENT=workflow_dispatch
export FIXTURE_WORKFLOW_NAME='Rehearse publisher build network'
export FIXTURE_WORKFLOW_PATH=.github/workflows/rehearse-release-network.yml
FIXTURE_ATTEMPT_ID="rel-r$(printf '%s' "$FIXTURE_RUN/$FIXTURE_ATTEMPT" | shasum -a 256 | cut -c1-13)"
export FIXTURE_ATTEMPT_ID
run
test "$(cat "$RECOVERY_TRACE")" = "$FIXTURE_RUN $FIXTURE_ATTEMPT"
FIXTURE_STATUS=in_progress run
test ! -s "$RECOVERY_TRACE"
FIXTURE_EVENT=push reject
FIXTURE_HEAD_BRANCH=review reject
FIXTURE_TAG=v1.0.0-rc.1 reject
FIXTURE_WORKFLOW_PATH=.github/workflows/release.yml reject

printf 'Publisher build network recovery fixtures passed.\n'
