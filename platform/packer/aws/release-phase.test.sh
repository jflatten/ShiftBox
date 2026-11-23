#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/bin"
cat > "$scratch/bin/aws" <<'AWS'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TRACE"
if [[ "$*" == 'sts get-caller-identity' ]]; then
  printf '%s\n' "$CALLER"
  exit 0
fi
exit 99
AWS
chmod +x "$scratch/bin/aws"
export PATH="$scratch/bin:$PATH" TRACE="$scratch/trace" RUNNER_TEMP="$scratch"
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export SHIFTBOX_RELEASE_STATE_BUCKET=shiftbox-release-state-111122223333-us-east-2
export GITHUB_EVENT_NAME=push GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REPOSITORY_ID=1457179716
export GITHUB_REPOSITORY_OWNER_ID=11561330 GITHUB_RUN_ID=123456 GITHUB_RUN_ATTEMPT=1
export GITHUB_ACTOR=jflatten GITHUB_TRIGGERING_ACTOR=jflatten GITHUB_REF_PROTECTED=true GITHUB_REF_TYPE=tag
SOURCE_COMMIT=$(git rev-parse HEAD)
export SHIFTBOX_RELEASE_MAINTAINERS=jflatten SOURCE_COMMIT
export GITHUB_SHA="$SOURCE_COMMIT" RELEASE_TAG=v2.0.1-rc.1 GITHUB_REF=refs/tags/v2.0.1-rc.1

# An unsupported phase or wrong state bucket must fail before AWS.
for bad in stable ../share; do
  if bash "$here/release-phase.sh" close "$bad" >/dev/null 2>&1; then exit 1; fi
done
if SHIFTBOX_RELEASE_STATE_BUCKET=unrelated bash "$here/release-phase.sh" close build >/dev/null 2>&1; then exit 1; fi
test ! -s "$TRACE"

# The phase controller must never use an arbitrary publisher role.
export CALLER='{"Account":"111122223333","Arn":"arn:aws:sts::111122223333:assumed-role/unrelated/session"}'
if bash "$here/release-phase.sh" close build >"$scratch/denied" 2>&1; then exit 1; fi
grep -q 'requires the exact publisher control session' "$scratch/denied"
diff -u <(printf '%s\n' 'sts get-caller-identity') "$TRACE"
printf 'Candidate phase admission fixtures passed.\n'

# Exercise build admission and teardown through the real script against a
# private Git fixture and mocked AWS and Terraform commands.
fixture="$scratch/repository"
mkdir -p "$fixture/platform/packer/aws" "$fixture/platform/release" "$fixture/platform/terraform/publisher/candidate-phase"
cp "$here"/{release-phase.sh,terraform-workflow.sh,workflow-log.jq} "$fixture/platform/packer/aws/"
cp "$here"/../../release/{publish.sh,manifest.sh} "$fixture/platform/release/"
printf 'terraform {}\n' > "$fixture/platform/terraform/publisher/candidate-phase/versions.tf"
git -C "$fixture" init -q
git -C "$fixture" add .
git -C "$fixture" -c commit.gpgsign=false -c user.name=Fixture -c user.email=fixture@example.invalid commit -qm 'Release fixture'
SOURCE_COMMIT=$(git -C "$fixture" rev-parse HEAD)
export SOURCE_COMMIT GITHUB_SHA="$SOURCE_COMMIT"
git -C "$fixture" -c tag.gpgsign=false tag "$RELEASE_TAG" "$SOURCE_COMMIT"
git -C "$fixture" update-ref refs/remotes/origin/main "$SOURCE_COMMIT"
export MOCK_ROOT="$scratch/cloud"
export PHASE_PREFIX=release-builds/123456/1/phases/build
ROLE_NAME="shiftbox-publisher-release-build-$(printf 123456/1 | shasum -a 256 | cut -c1-16)"
export ROLE_NAME
export ROLE_ARN="arn:aws:iam::111122223333:role/shiftbox-release/$ROLE_NAME"
export CALLER='{"Account":"111122223333","Arn":"arn:aws:sts::111122223333:assumed-role/shiftbox-publisher-release-network/shiftbox-release-network-123456-1"}'
mkdir -p "$MOCK_ROOT/$PHASE_PREFIX" "$RUNNER_TEMP/shiftbox-publisher-network"
expires=$(jq -nr 'now+3600|todateiso8601')
attempt="rel-$(printf 123456/1 | shasum -a 256 | cut -c1-14)"
jq -n --arg source "$SOURCE_COMMIT" --arg tag "$RELEASE_TAG" --arg expires "$expires" --arg attempt "$attempt" '
  {run_id:"123456",run_attempt:"1",source_commit:$source,candidate_tag:$tag,expires_at:$expires,attempt_id:$attempt,
   source_ami:"ami-00000000000000001",source_snapshot:"snap-00000000000000002"}
' > "$RUNNER_TEMP/shiftbox-publisher-network/lease.json"
jq -n --arg expires "$expires" --arg attempt "$attempt" '
  {attempt_id:$attempt,deadline:$expires,source_ami:"ami-00000000000000001",vpc_id:"vpc-00000000000000001",
   private_subnet_id:"subnet-00000000000000002",private_subnet_cidr:"10.98.0.64/26",
   builder_group_id:"sg-00000000000000003",endpoint_id:"eice-00000000000000004"}
' > "$RUNNER_TEMP/shiftbox-publisher-network/network.json"

cat > "$scratch/bin/aws" <<'AWS'
#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
  'sts get-caller-identity') printf '%s\n' "$CALLER" ;;
  's3api get-bucket-location') printf '%s\n' '{"LocationConstraint":"us-east-2"}' ;;
  's3api get-public-access-block')
    printf '%s\n' '{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}}' ;;
  's3api list-objects-v2')
    prefix=${!#}
    if [[ -f "$MOCK_ROOT/$prefix" ]]; then jq -n --arg key "$prefix" '{Contents:[{Key:$key}]}'
    else printf '%s\n' '{}'; fi ;;
  's3api get-object'|'s3api put-object')
    call=$2
    shift 2
    while [[ "${1:-}" != --key ]]; do shift; done
    key=$2
    if [[ "$call" == put-object ]]; then
      [[ "$3" == --body && "$5" == --if-none-match && "$6" == '*' && "$8" == AES256 ]]
      test ! -e "$MOCK_ROOT/$key"
      cp "$4" "$MOCK_ROOT/$key"
    else
      cp "$MOCK_ROOT/$key" "$3"
    fi
    printf '%s\n' '{"ServerSideEncryption":"AES256","VersionId":"fixture-version"}' ;;
  'iam get-role')
    if [[ ${DENY_PHASE_LOOKUP:-} == true ]]; then
      printf 'An error occurred (AccessDenied) when calling the GetRole operation\n' >&2
      exit 254
    fi
    if [[ -f "$MOCK_ROOT/role.json" ]]; then cat "$MOCK_ROOT/role.json"
    else printf '%s\n' NoSuchEntity >&2; exit 254; fi ;;
  'iam list-role-policies')
    if [[ "$*" == *--query* ]]; then printf '%s\n' 1
    else printf '%s\n' '{"PolicyNames":["CandidateWorkspaceAMI"]}'; fi ;;
  *) exit 99 ;;
esac
AWS

cat > "$scratch/bin/terraform" <<'TF'
#!/usr/bin/env bash
set -euo pipefail
shift
case "$1" in
  init)
    config=${!#}
    grep -q "use_lockfile         = true" "${config#*=}"
    grep -q "encrypt              = true" "${config#*=}" ;;
  plan)
    [[ " $* " != *' -destroy '* ]] || touch "$MOCK_ROOT/retire"
    # A plan sent to stderr still appears without exposing its arbitrary message.
    printf '%s\n' '{"type":"planned_change","change":{"resource":{"addr":"aws_iam_role.ami_candidate"},"action":"create"},"@message":"PRIVATE_PHASE_MESSAGE"}' >&2 ;;
  show)
    if [[ -f "$MOCK_ROOT/retire" ]]; then
      jq -n --arg arn "$ROLE_ARN" --arg name "$ROLE_NAME" --arg bad "${FOREIGN_DESTROY:-}" '
        {resource_changes:[{address:"aws_iam_role.ami_candidate",change:{actions:["delete"],before:{arn:$arn}}},
        {address:"aws_iam_role_policy.ami_candidate",change:{actions:["delete"],before:{role:$name,name:"CandidateWorkspaceAMI"}}}]} |
        if $bad=="true" then .resource_changes[0].address="aws_s3_bucket.foreign" else . end
      '
    else
      jq -n --arg name "$ROLE_NAME" '
        {resource_changes:[{address:"aws_iam_role.ami_candidate",change:{actions:["create"],after:{
          name:$name,path:"/shiftbox-release/",permissions_boundary:"arn:aws:iam::111122223333:policy/shiftbox-publisher-release-build-boundary"}}},
          {address:"aws_iam_role_policy.ami_candidate",change:{actions:["create"]}}]}
      '
    fi ;;
  apply)
    if [[ -f "$MOCK_ROOT/retire" ]]; then
      rm "$MOCK_ROOT/role.json"
      touch "$MOCK_ROOT/destroyed"
    else
      jq --arg arn "$ROLE_ARN" '
        . as $l | {Role:{Arn:$arn,PermissionsBoundary:{PermissionsBoundaryArn:"arn:aws:iam::111122223333:policy/shiftbox-publisher-release-build-boundary"},
        Tags:({project:"shiftbox",cluster_name:"shiftbox-publisher",release_run_id:.execution.run_id,
          release_run_attempt:.execution.run_attempt,release_phase:.phase,environment:("release-"+.candidate_tag)}|
          to_entries|map({Key:.key,Value:.value})),
        AssumeRolePolicyDocument:{Statement:[{Effect:"Allow",Action:"sts:AssumeRoleWithWebIdentity",
          Condition:{StringEquals:{"sts:RoleSessionName":"shiftbox-ami-build-123456-1","token.actions.githubusercontent.com:ref":("refs/tags/"+$l.candidate_tag)},
          DateLessThan:{"aws:CurrentTime":$l.execution.expires_at}}}]}}}
      ' "$MOCK_ROOT/$PHASE_PREFIX/phase.json" > "$MOCK_ROOT/role.json"
      touch "$MOCK_ROOT/$PHASE_PREFIX/terraform.tfstate"
    fi ;;
  output) printf '%s\n' "$ROLE_ARN" ;;
  state) printf '%s\n' aws_iam_role.ami_candidate aws_iam_role_policy.ami_candidate ;;
  *) exit 99 ;;
esac
TF
chmod +x "$scratch/bin/terraform"
# The workflow's preflight must detect missing lookups without reserving any
# state or creating a role. A successful lookup of an existing role also blocks.
for phase in build copy share; do
  mkdir -p "$MOCK_ROOT/release-builds/123456/1/phases/$phase"
  bash "$fixture/platform/packer/aws/release-phase.sh" check "$phase"
  test ! -e "$MOCK_ROOT/release-builds/123456/1/phases/$phase/phase.json"
done
if DENY_PHASE_LOOKUP=true bash "$fixture/platform/packer/aws/release-phase.sh" check build >"$scratch/lookup.log" 2>&1; then
  exit 1
fi
grep -Fxq 'AWS diagnostic: GetRole AccessDenied' "$scratch/lookup.log"
test ! -e "$MOCK_ROOT/$PHASE_PREFIX/phase.json"
printf '{}\n' > "$MOCK_ROOT/role.json"
if bash "$fixture/platform/packer/aws/release-phase.sh" check build >/dev/null 2>&1; then exit 1; fi
rm "$MOCK_ROOT/role.json"

# Terraform progress reaches stderr without corrupting the ARN on stdout.
test "$(bash "$fixture/platform/packer/aws/release-phase.sh" open build 2>"$scratch/phase.log")" = "$ROLE_ARN"
grep -Fxq 'Terraform planned create aws_iam_role.ami_candidate' "$scratch/phase.log"
if grep -Fq 'PRIVATE_PHASE_MESSAGE' "$scratch/phase.log"; then exit 1; fi
if bash "$fixture/platform/packer/aws/release-phase.sh" open build >/dev/null 2>&1; then exit 1; fi

# Even with an exact role and state, a destroy plan touching a foreign
# resource must be rejected before Terraform applies it.
if FOREIGN_DESTROY=true bash "$fixture/platform/packer/aws/release-phase.sh" close build >/dev/null 2>&1; then exit 1; fi
test ! -e "$MOCK_ROOT/destroyed"
test -s "$MOCK_ROOT/role.json"
bash "$fixture/platform/packer/aws/release-phase.sh" close build
test -e "$MOCK_ROOT/destroyed"
test -f "$MOCK_ROOT/$PHASE_PREFIX/phase-retired.json"
test ! -e "$MOCK_ROOT/role.json"
printf 'Candidate phase encrypted state and exact apply and destroy fixtures passed.\n'
