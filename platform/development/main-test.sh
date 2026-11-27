#!/usr/bin/env bash
# Build and deploy one disposable main-branch test without publishing a release.
set -euo pipefail
umask 077

account=888293065132
region=us-east-2
stack=sbtest-main
registry=ghcr.io/jflatten/shiftbox-main-test
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

fail() { printf 'AWS test from main: %s\n' "$1" >&2; exit 1; }

source_guard() {
  [[ ${GITHUB_REPOSITORY:-} == jflatten/ShiftBox && ${GITHUB_EVENT_NAME:-} == workflow_dispatch &&
     ${GITHUB_REF:-} == refs/heads/main && ${GITHUB_ACTOR_ID:-} == 11561330 &&
     ${GITHUB_TRIGGERING_ACTOR:-} == jflatten && ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ &&
     ${GITHUB_RUN_ID:-} =~ ^[1-9][0-9]*$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[1-9][0-9]*$ ]] ||
    fail 'use the owner-dispatched workflow on main'
  [[ $(git rev-parse HEAD) == "$GITHUB_SHA" && -z $(git status --porcelain) ]] ||
    fail 'the checkout must be clean and match the dispatched commit'
  [[ $(git ls-remote --exit-code origin refs/heads/main) == "$GITHUB_SHA"$'\trefs/heads/main' ]] ||
    fail 'main moved after dispatch. Start a new test from its current tip'
}

cloud() { aws --region "$region" --no-cli-pager "$@"; }

private_bucket() {
  [[ ${TEST_BUCKET:-} =~ ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$ ]] || fail 'configure a private Ohio staging bucket'
  [[ $(cloud sts get-caller-identity --query Account --output text) == "$account" ]] || fail 'use the Ohio testing account'
  [[ $(cloud s3api get-bucket-location --bucket "$TEST_BUCKET" --expected-bucket-owner "$account" \
    --query LocationConstraint --output text) == "$region" ]] || fail 'use a staging bucket in Ohio'
  cloud s3api get-public-access-block --bucket "$TEST_BUCKET" --expected-bucket-owner "$account" |
    jq -e '.PublicAccessBlockConfiguration | length == 4 and all(.[]; . == true)' >/dev/null ||
    fail 'enable all four blocks on public access for the staging bucket'
  [[ $(cloud s3api get-bucket-versioning --bucket "$TEST_BUCKET" --expected-bucket-owner "$account" \
    --query Status --output text) == Enabled ]] || fail 'enable staging bucket versioning'
}

stage_file() {
  cloud s3api put-object --bucket "$TEST_BUCKET" --expected-bucket-owner "$account" \
    --key "$prefix/$1" --body "$2" --server-side-encryption AES256 --if-none-match '*' |
    jq -er '.VersionId | strings | select(length > 0 and . != "null")'
}

private_package() {
  [[ $(gh api /users/jflatten/packages/container/shiftbox-main-test --jq .visibility) == private ]] ||
    fail 'pre-create the private shiftbox-main-test package and grant this repository Actions access'
}

verify_destination() {
  local response
  if response=$(cloud cloudformation describe-stacks --stack-name "$stack" 2>&1); then
    fail "delete the previous $stack test and its retained resources before dispatching another run"
  fi
  [[ $response == *'(ValidationError)'* && $response == *"Stack with id $stack does not exist"* ]] ||
    fail 'could not verify that the test stack name is available'
}

source_guard
operation=${1:-}
[[ $operation != source ]] || exit 0
private_bucket
prefix="main-test/$GITHUB_RUN_ID/$GITHUB_RUN_ATTEMPT"
if [[ $operation == stage-helper ]]; then
  [[ -f ${2:-} && -s $2 ]] || fail 'the macOS helper package is missing'
  helper_version=$(stage_file ShiftBox-Local-Helper.dmg "$2")
  helper_sha=$(shasum -a 256 < "$2")
  printf 'version=%s\nsha256=%s\n' "$helper_version" "${helper_sha%% *}" >> "$GITHUB_OUTPUT"
  exit 0
fi
[[ $operation == deploy ]] || fail 'use source, stage-helper, or deploy'
verify_destination
private_package
cloud secretsmanager describe-secret --secret-id shiftbox-main-test-ghcr |
  jq -e '.Name == "shiftbox-main-test-ghcr" and .DeletedDate == null' >/dev/null ||
  fail 'configure the private ECS registry credential in Ohio'

scratch=$(mktemp -d "${RUNNER_TEMP:?}/shiftbox-main-test.XXXXXX")
bridge_pid=''
docker_builder=''
pending_change=''
key_imported=false
key_name="$stack-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
cleanup() {
  if [[ -n $pending_change ]]; then
    cloud cloudformation delete-change-set --change-set-name "$pending_change" >/dev/null ||
      printf 'Remove the unexecuted test change set %s.\n' "$pending_change" >&2
  fi
  if [[ -n $docker_builder ]]; then
    docker buildx rm "$docker_builder" >/dev/null 2>&1 || true
  fi
  if [[ -n $bridge_pid ]]; then
    kill "$bridge_pid" 2>/dev/null || true
    wait "$bridge_pid" 2>/dev/null || true
  fi
  if $key_imported; then
    cloud ec2 delete-key-pair --key-name "$key_name" >/dev/null ||
      printf 'Remove the disposable Ohio build key %s.\n' "$key_name" >&2
  fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

runtime="v0.0.0-rc.${GITHUB_RUN_ID}${GITHUB_RUN_ATTEMPT}"
[[ ${TEST_HELPER_SHA256:-} =~ ^[0-9a-f]{64}$ && -n ${TEST_HELPER_VERSION:-} &&
   ${TEST_GUEST_UPDATE_PUBLIC_KEY:-} =~ ^[A-Za-z0-9+/]{43}=$ &&
   ${TEST_HOST_AMI:-} =~ ^ami-[0-9a-f]{17}$ &&
   ${TEST_POSTGRES_IMAGE:-} =~ ^docker\.io/library/postgres:17-(bookworm|bullseye|trixie)@sha256:[0-9a-f]{64}$ ]] ||
  fail 'configure the pinned ECS host, PostgreSQL, and guest-update key inputs'

jq -ce '
  select(keys == ["builder_group_id","endpoint_id","private_subnet_cidr","private_subnet_id","source_ami","vpc_id"]) |
  select(.builder_group_id | test("^sg-[0-9a-f]{17}$")) |
  select(.endpoint_id | test("^eice-[0-9a-f]{17}$")) |
  select(.private_subnet_id | test("^subnet-[0-9a-f]{17}$")) |
  select(.source_ami | test("^ami-[0-9a-f]{17}$")) |
  select(.vpc_id | test("^vpc-[0-9a-f]{17}$"))
' <<< "${TEST_BUILD_NETWORK:-}" > "$scratch/network.json" || fail 'configure the private Packer network'
jq -ce '
  select(type == "object" and all(.[]; type == "string") and
         (keys - ["AcmeContactEmail","AcmeTermsAccepted","BrowserAccess","DnsPlacement","DnsSuffix",
                  "OperatorCidr","PublicDnsZone","PublicIngressCidr","ServiceSubnet","WorkspaceSubnet"] | length == 0)) |
  select(.ServiceSubnet | test("^subnet-[0-9a-f]{17}$")) |
  select(.WorkspaceSubnet | test("^subnet-[0-9a-f]{17}$")) |
  select(.BrowserAccess == "Private" or (.BrowserAccess == "Public" and (.PublicIngressCidr | length > 0)))
' <<< "${TEST_STACK_PARAMETERS:-}" > "$scratch/stack.json" || fail 'configure the Ohio test stack parameters'

mkdir "$scratch/helper" "$scratch/docker"
cloud s3api get-object --bucket "$TEST_BUCKET" --expected-bucket-owner "$account" \
  --key "$prefix/ShiftBox-Local-Helper.dmg" --version-id "$TEST_HELPER_VERSION" \
  "$scratch/helper/ShiftBox-Local-Helper.dmg" >/dev/null
printf '%s  %s\n' "$TEST_HELPER_SHA256" "$scratch/helper/ShiftBox-Local-Helper.dmg" | sha256sum --check

printf '%s' "$TEST_GUEST_UPDATE_PUBLIC_KEY" | openssl base64 -A -d > "$scratch/guest-update.pub"
[[ $(wc -c < "$scratch/guest-update.pub") -eq 32 ]] || fail 'the guest-update key must be 32 raw bytes'
ssh-keygen -q -t ed25519 -N '' -f "$scratch/builder-key"
cloud ec2 import-key-pair --key-name "$key_name" --public-key-material "fileb://$scratch/builder-key.pub" \
  --tag-specifications "ResourceType=key-pair,Tags=[{Key=shiftbox-project,Value=shiftbox},{Key=shiftbox-cluster-name,Value=$stack}]" >/dev/null
key_imported=true

network="$scratch/network.json"
python3 platform/packer/aws/eice-socks.py --private-test --account-id "$account" --candidate-tag "$runtime" \
  --endpoint-id "$(jq -er .endpoint_id "$network")" --vpc-id "$(jq -er .vpc_id "$network")" \
  --subnet-id "$(jq -er .private_subnet_id "$network")" --subnet-cidr "$(jq -er .private_subnet_cidr "$network")" \
  --security-group-id "$(jq -er .builder_group_id "$network")" --attempt-id "$stack" \
  --port-file "$scratch/eice.port" &
bridge_pid=$!
for ((check=0; check<100; check++)); do
  [[ ! -s "$scratch/eice.port" ]] || break
  kill -0 "$bridge_pid" 2>/dev/null || fail 'the private build tunnel rejected the configured endpoint'
  sleep 0.2
done
[[ -s "$scratch/eice.port" ]] || fail 'the private build tunnel did not become ready'
jq --arg key "$scratch/builder-key" --arg key_name "$key_name" --arg stack "$stack" --arg runtime "$runtime" \
  --argjson port "$(cat "$scratch/eice.port")" '
  {region:"us-east-2",source_ami,build_subnet_id:.private_subnet_id,build_security_group_id:.builder_group_id,
   cluster_name:$stack,environment:$runtime,build_key_pair:$key_name,build_private_key_file:$key,
   build_ssh_proxy_host:"127.0.0.1",build_ssh_proxy_port:$port}
' "$network" > "$scratch/build.pkrvars.json"
timeout --signal=TERM --kill-after=30s 45m env PUBLIC_AMI_BUILD=0 RELEASE_CANDIDATE="$runtime" GUEST_AGENT_RELEASE="$runtime" \
  platform/packer/aws/build-image.sh "$runtime" "$scratch/guest-update.pub" "$scratch/build.pkrvars.json" |
  tee "$scratch/packer.log" | jq --unbuffered -Rr --arg tool Packer -f platform/packer/aws/workflow-log.jq
catalog=$(sed -n 's/^AMI provenance manifest: //p' "$scratch/packer.log")
test -f "$catalog"
jq -ce '
  def build:
    {artifact_id,builder_type,packer_run_uuid,custom_data:
        (.custom_data | del(.publisherAccountId,.publicBuild))};
  {builds:(.builds | map(build)),last_run_uuid}
' "$catalog" > "$scratch/catalog.json"
catalog="$scratch/catalog.json"
[[ $(wc -c < "$catalog") -le 1536 ]] || fail 'the catalog is too large for launch discovery'
ami=$(jq -er --arg source "$GITHUB_SHA" --arg runtime "$runtime" '
  .last_run_uuid as $run | .builds | select(length == 1) | .[0] |
  select(.packer_run_uuid == $run and .builder_type == "amazon-ebs" and
         .custom_data.sourceRevision == $source and .custom_data.guestAgentRelease == $runtime and
         .custom_data.releaseId == $runtime) |
  .artifact_id | select(test("^us-east-2:ami-[0-9a-f]{17}$")) | split(":")[1]
' "$catalog")
snapshot=$(cloud ec2 describe-images --owners "$account" --image-ids "$ami" |
  jq -er '.Images | select(length == 1) | .[0] | select(.Public == false and .State == "available" and
    .Architecture == "x86_64" and .RootDeviceName == "/dev/sda1") |
    [.BlockDeviceMappings[] | select(.DeviceName == "/dev/sda1" and .Ebs.Encrypted and .Ebs.VolumeSize == 32)] |
    select(length == 1) | .[0].Ebs.SnapshotId | select(test("^snap-[0-9a-f]{17}$"))')
cloud ec2 describe-image-attribute --image-id "$ami" --attribute launchPermission |
  jq -e '(.LaunchPermissions // []) == []' >/dev/null || fail 'the test AMI must have no external launch grants'

source_guard
private_package
export DOCKER_CONFIG="$scratch/docker"
printf '%s\n' "$GH_TOKEN" | docker login ghcr.io --username "$GITHUB_ACTOR" --password-stdin
docker_builder="$stack-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
docker buildx create --name "$docker_builder" --driver docker-container --bootstrap >/dev/null
docker buildx build --builder "$docker_builder" --platform linux/amd64 --file platform/container/Dockerfile \
  --build-arg "RELEASE_CANDIDATE=$runtime" --build-context "macos-helper=$scratch/helper" \
  --label "org.opencontainers.image.source=https://github.com/$GITHUB_REPOSITORY" \
  --label "org.opencontainers.image.revision=$GITHUB_SHA" \
  --metadata-file "$scratch/controller.json" --tag "$registry:main-$GITHUB_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT" --push .
private_package
digest=$(jq -er '."containerimage.digest" | select(test("^sha256:[0-9a-f]{64}$"))' "$scratch/controller.json")

jq -cn --arg controller "$registry@$digest" --arg postgres "$TEST_POSTGRES_IMAGE" --arg host "$TEST_HOST_AMI" \
  --arg ami "$ami" --arg publisher "$account" --arg snapshot "$snapshot" --arg source "$GITHUB_SHA" --arg runtime "$runtime" \
  --arg sha "$(sha256sum "$catalog" | cut -d' ' -f1)" --rawfile catalog "$catalog" \
  --arg base_ami "$(jq -er .source_ami "$network")" '
  {Region:"us-east-2",ControllerImage:$controller,PostgresImage:$postgres,HostAmi:$host,WorkspaceAmi:$ami,
   PublisherAccount:$publisher,WorkspaceRootDevice:"/dev/sda1",WorkspaceSnapshot:$snapshot,
   WorkspaceSourceAmi:$base_ami,WorkspaceSourceRegion:"us-east-2",WorkspaceSourceRevision:$source,
   WorkspaceRelease:$runtime,GuestRelease:$runtime,WorkspaceArchitecture:"amd64",WorkspaceMinimumDiskGiB:"32",
   WorkspaceCatalogSHA256:$sha,WorkspaceStarterCatalog:($catalog | @base64)}
' > "$scratch/pins.json"
jq --arg pins "$(cat "$scratch/pins.json")" '
  . + {ReleasePins:$pins} | to_entries | map({ParameterKey:.key,ParameterValue:.value})
' "$scratch/stack.json" > "$scratch/parameters.json"

source_guard
template=platform/cloudformation/aws/template.yaml
git show "$GITHUB_SHA:$template" | cmp -s - "$template" || fail 'the template differs from the dispatched commit'
template_version=$(stage_file template.yaml "$template")
template_url="https://$TEST_BUCKET.s3.$region.amazonaws.com/$prefix/template.yaml?versionId=$(jq -rn --arg version "$template_version" '$version | @uri')"
change_name="main-test-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
cloud cloudformation create-change-set --stack-name "$stack" --change-set-type CREATE --change-set-name "$change_name" \
  --template-url "$template_url" --parameters "file://$scratch/parameters.json" --capabilities CAPABILITY_NAMED_IAM \
  --role-arn "arn:aws:iam::$account:role/shiftbox-main-test-cfn" \
  --tags "Key=shiftbox-project,Value=shiftbox" "Key=shiftbox-cluster-name,Value=$stack" \
  "Key=shiftbox-source-commit,Value=$GITHUB_SHA" "Key=shiftbox-test-run,Value=$GITHUB_RUN_ID" > "$scratch/change.json"
change=$(jq -er .Id "$scratch/change.json")
stack_id=$(jq -er .StackId "$scratch/change.json")
[[ $change == "arn:aws:cloudformation:$region:$account:changeSet/$change_name/"* &&
   $stack_id == "arn:aws:cloudformation:$region:$account:stack/$stack/"* ]] || fail 'unexpected change-set or stack identity'
pending_change=$change
cloud cloudformation wait change-set-create-complete --stack-name "$stack_id" --change-set-name "$change"
cloud cloudformation describe-change-set --stack-name "$stack_id" --change-set-name "$change" |
  jq -e --arg stack "$stack_id" --arg change "$change" --slurpfile expected "$scratch/parameters.json" '
    .StackId == $stack and .ChangeSetId == $change and .Status == "CREATE_COMPLETE" and .ExecutionStatus == "AVAILABLE" and
    (.Parameters as $parameters | all($expected[0][]; . as $pin |
       any($parameters[]; .ParameterKey == $pin.ParameterKey and .ParameterValue == $pin.ParameterValue))) and
    (.Changes | length > 0 and all(.[]; .Type == "Resource" and .ResourceChange.Action == "Add" and
      .ResourceChange.ResourceType != "AWS::CloudFormation::Stack"))
  ' >/dev/null || fail 'the pending change set differs from the requested test stack'
cloud cloudformation get-template --stack-name "$stack_id" --change-set-name "$change" --template-stage Original |
  jq -ej '.TemplateBody | strings' | cmp -s - "$template" || fail 'the staged template differs from the checked-in YAML'
source_guard
cloud cloudformation execute-change-set --stack-name "$stack_id" --change-set-name "$change"
pending_change=''
cloud cloudformation wait stack-create-complete --stack-name "$stack_id"
{
  printf '## Ohio AWS test\n\n'
  printf 'Source commit: `%s`\n\n' "$GITHUB_SHA"
  printf '[Open the testing stack in CloudFormation](https://%s.console.aws.amazon.com/cloudformation/home?region=%s#/stacks/stackinfo?stackId=%s)\n\n' \
    "$region" "$region" "$(jq -rn --arg stack "$stack_id" '$stack | @uri')"
  printf 'Use ManagementOrigin and SetupClaimSecretArn from the Outputs tab to complete first login.\n'
} >> "$GITHUB_STEP_SUMMARY"
