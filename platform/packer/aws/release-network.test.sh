#!/usr/bin/env bash
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir -p "$scratch/bin" "$scratch/runner" "$scratch/s3"
export RUNNER_TEMP="$scratch/runner" STORE="$scratch/s3" TRACE="$scratch/trace" REAL_GIT
REAL_GIT=$(command -v git)
export PATH="$scratch/bin:$PATH"
SOURCE_COMMIT=$("$REAL_GIT" -C "$root" rev-parse HEAD)
export SOURCE_COMMIT
export RELEASE_TAG=v2.4.0-rc.2 GITHUB_RUN_ID=10001 GITHUB_RUN_ATTEMPT=1
export GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REPOSITORY_ID=1457179716 GITHUB_REPOSITORY_OWNER_ID=11561330
export GITHUB_ACTOR=jflatten GITHUB_TRIGGERING_ACTOR=jflatten GITHUB_EVENT_NAME=push GITHUB_REF_TYPE=tag GITHUB_REF_PROTECTED=true
export SHIFTBOX_RELEASE_MAINTAINERS=jflatten GITHUB_SHA="$SOURCE_COMMIT" GITHUB_REF="refs/tags/$RELEASE_TAG"
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export SHIFTBOX_RELEASE_STATE_BUCKET=shiftbox-release-state-111122223333-us-east-2
export SHIFTBOX_RELEASE_SOURCE_AMI=ami-00000000000000009
export RESOURCE_ADDRESSES="$scratch/addresses"
cat > "$RESOURCE_ADDRESSES" <<'ADDRESSES'
aws_cloudwatch_log_group.expiry
aws_ec2_instance_connect_endpoint.builder
aws_eip.nat
aws_iam_role.expiry
aws_iam_role.expiry_scheduler
aws_iam_role_policy.expiry
aws_iam_role_policy.expiry_scheduler
aws_internet_gateway.build
aws_key_pair.build
aws_lambda_function.expiry
aws_lambda_function_event_invoke_config.expiry
aws_nat_gateway.build
aws_route.private_default
aws_route.public_default
aws_route_table.private
aws_route_table.public
aws_route_table_association.private
aws_route_table_association.public
aws_scheduler_schedule.expiry["final"]
aws_scheduler_schedule.expiry["first"]
aws_scheduler_schedule.expiry["fourth"]
aws_scheduler_schedule.expiry["second"]
aws_scheduler_schedule.expiry["third"]
aws_scheduler_schedule_group.expiry
aws_security_group.builder
aws_security_group.endpoint
aws_subnet.private
aws_subnet.public
aws_vpc.build
aws_vpc_security_group_egress_rule.builder_https
aws_vpc_security_group_egress_rule.endpoint_ssh
aws_vpc_security_group_ingress_rule.builder_ssh
terraform_data.attempt_window
ADDRESSES
cat > "$scratch/bin/git" <<'STUB'
#!/usr/bin/env bash
set -eu
if [[ "$*" == *"refs/tags/$RELEASE_TAG"* && "$*" == *'rev-parse'* ]]; then
  printf '%s\n' "$SOURCE_COMMIT"
  exit 0
fi
if [[ "$1" == merge-base ]]; then exit 0; fi
exec "$REAL_GIT" "$@"
STUB
cat > "$scratch/bin/aws" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
test "${AWS_REGION:-}" = us-east-2
test "${AWS_IGNORE_CONFIGURED_ENDPOINT_URLS:-}" = true
case "$1 $2" in
  'sts get-caller-identity')
    if [[ "$*" == *'--query Arn'* ]]; then
      if [[ ${SHIFTBOX_RELEASE_RECOVERY:-} == true ]]; then
        role=shiftbox-publisher-release-network-cleanup
        session=shiftbox-release-cleanup
      elif [[ ${SHIFTBOX_RELEASE_NETWORK_REHEARSAL:-} == true ]]; then
        role=shiftbox-publisher-release-network-rehearsal
        session=shiftbox-release-rehearsal
      else
        role=shiftbox-publisher-release-network
        session=shiftbox-release-network
      fi
      printf 'arn:aws:sts::111122223333:assumed-role/%s/%s-%s-%s\n'         "${AWS_TEST_ROLE:-$role}" "$session" "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT"
    else
      echo "${AWS_TEST_ACCOUNT:-111122223333}"
    fi ;;
  's3api get-bucket-location') echo '{"LocationConstraint":"us-east-2"}' ;;
  's3api get-public-access-block')
    if [[ ${PUBLIC_BACKEND:-} == true ]]; then
      echo '{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":false,"RestrictPublicBuckets":true}}'
    else
      echo '{"PublicAccessBlockConfiguration":{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}}'
    fi ;;
  's3api get-bucket-policy-status') echo '{"PolicyStatus":{"IsPublic":false}}' ;;
  's3api get-bucket-ownership-controls') echo '{"OwnershipControls":{"Rules":[{"ObjectOwnership":"BucketOwnerEnforced"}]}}' ;;
  's3api get-bucket-versioning') echo '{"Status":"Enabled"}' ;;
  's3api get-bucket-encryption') echo '{"ServerSideEncryptionConfiguration":{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}}' ;;
  's3api list-objects-v2')
    shift 2
    while [[ $# -gt 0 ]]; do
      case "$1" in --prefix) key=$2; shift ;; esac
      shift
    done
    if test -f "$STORE/$key"; then
      jq -nc --arg key "$key" '{Contents:[{Key:$key}]}'
    else
      echo '{"Contents":[]}'
    fi ;;
  's3api get-object')
    dest=${!#}
    shift 2
    while [[ $# -gt 0 ]]; do
      case "$1" in --key) key=$2; shift ;; esac
      shift
    done
    cp "$STORE/$key" "$dest"
    echo '{"ServerSideEncryption":"AES256"}' ;;
  's3api put-object')
    [[ " $* " == *' --server-side-encryption AES256 '* && " $* " == *' --if-none-match * '* ]]
    shift 2
    while [[ $# -gt 0 ]]; do
      case "$1" in --key) key=$2; shift ;; --body) body=$2; shift ;; esac
      shift
    done
    if test -e "$STORE/$key"; then exit 3; fi
    mkdir -p "$(dirname "$STORE/$key")"
    cp "$body" "$STORE/$key"
    printf 'put %s\n' "${key##*/}" >> "$TRACE"
    echo '{"ServerSideEncryption":"AES256","VersionId":"fixture-version"}' ;;
  'ec2 describe-availability-zones') echo '{"AvailabilityZones":[{"ZoneId":"use2-az1","State":"available"}]}' ;;
  'ec2 describe-instance-type-offerings') echo '{"InstanceTypeOfferings":[{"Location":"use2-az1","InstanceType":"t3.medium"}]}' ;;
  'ec2 describe-vpcs')
    if [[ ${OVERLAPS:-} == true ]]; then
      echo '{"Vpcs":[{"CidrBlock":"10.98.0.0/24","CidrBlockAssociationSet":[]}]}'
    else
      echo '{"Vpcs":[]}'
    fi ;;
  'ec2 describe-images') echo '{"Images":[{"ImageId":"ami-00000000000000009","OwnerId":"099720109477","State":"available","Public":true,"Architecture":"x86_64","RootDeviceType":"ebs","VirtualizationType":"hvm","RootDeviceName":"/dev/sda1","BlockDeviceMappings":[{"DeviceName":"/dev/sda1","Ebs":{"Encrypted":false,"SnapshotId":"snap-00000000000000009","VolumeSize":8}}]}]}' ;;
  'ec2 describe-instances')
    if [[ ${ACTIVE_INSTANCE:-} == true ]]; then
      echo '{"Reservations":[{"Instances":[{"State":{"Name":"running"}}]}]}'
    else
      echo '{"Reservations":[]}'
    fi ;;
  'ec2 describe-volumes') echo '{"Volumes":[]}' ;;
  'ec2 describe-nat-gateways')
    if [[ ${LEFTOVER_NAT:-} == true ]]; then echo '{"NatGateways":[{"State":"available"}]}'; else echo '{"NatGateways":[]}'; fi ;;
  'ec2 describe-addresses') echo '{"Addresses":[]}' ;;
  'ec2 describe-instance-connect-endpoints')
    if [[ -f "$STORE/orphan-endpoint" ]]; then
      jq -cn --arg vpc "${ORPHAN_VPC:-vpc-00000000000000001}" --arg attempt "$(cat "$STORE/orphan-endpoint")" --arg tag "$RELEASE_TAG" '
        {InstanceConnectEndpoints:[{InstanceConnectEndpointId:"eice-00000000000000003",State:"create-failed",
          VpcId:$vpc,SubnetId:"subnet-00000000000000002",Tags:[{Key:"project",Value:"shiftbox"},
            {Key:"cluster_name",Value:"shiftbox-publisher"},{Key:"publisher_attempt",Value:$attempt},
            {Key:"environment",Value:("release-"+$tag)}]}]}'
    else
      echo '{"InstanceConnectEndpoints":[]}'
    fi ;;
  'ec2 describe-key-pairs') echo '{"KeyPairs":[]}' ;;
  'ec2 describe-network-interfaces') echo '{"NetworkInterfaces":[]}' ;;
  *) printf 'unexpected AWS call: %s %s\n' "$1" "$2" >&2; exit 1 ;;
esac
STUB
cat > "$scratch/bin/terraform" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
cd "${1#-chdir=}"
shift
cmd=$1
shift
case "$cmd" in
  init)
    test -f terraform-s3.tf
    config=${*: -1}
    config=${config#-backend-config=}
    grep -Eq 'use_lockfile[[:space:]]*=[[:space:]]*true' "$config"
    grep -Eq 'encrypt[[:space:]]*=[[:space:]]*true' "$config"
    key=$(sed -nE 's/^[[:space:]]*key[[:space:]]*=[[:space:]]*"([^"]+)"$/\1/p' "$config")
    # Terraform's workspace discovery must stay inside the rehearsal role's S3 list grant.
    if [[ "$key" == release-builds/rehearsals/* ]]; then
      grep -Eq '^workspace_key_prefix[[:space:]]*= "release-builds/rehearsals"$' "$config"
    fi
    printf '%s\n' "$key" > .fixture-state-key
    ;;
  plan)
    printf 'plan\n' >> "$TRACE"
    mode=create
    for flag; do
      case "$flag" in -destroy) mode=delete ;; -out=*) output=${flag#-out=} ;; -var-file=*) inputs=${flag#-var-file=} ;; esac
    done
    attempt=$(jq -er '.attempt_id' "$inputs")
    jq -Rn --arg mode "$mode" --arg attempt "$attempt" --slurpfile inputs "$inputs" '
      [inputs] as $addresses |
      {variables:($inputs[0]|map_values({value:.})),resource_changes:[$addresses[] as $address |
        {mode:"managed",address:$address,type:($address|split(".")[0]),change:{
          actions:[$mode], before:{id:({"aws_vpc.build":"vpc-00000000000000001",
            "aws_subnet.private":"subnet-00000000000000002",
            "aws_ec2_instance_connect_endpoint.builder":"eice-00000000000000003"}[$address] // $address),
            tags_all:{project:"shiftbox",cluster_name:"shiftbox-publisher",
            environment:("release-"+$inputs[0].candidate_tag),publisher_attempt:$attempt}},
          after:{id:$address}}}]}
    ' < "$RESOURCE_ADDRESSES" > "$output"
    if [[ ${FOREIGN_PLAN:-} == true ]]; then
      jq '.resource_changes[0].address="aws_ami.unowned"' "$output" > "$output.tmp"
      mv "$output.tmp" "$output"
    fi
    ;;
  show)
    if [[ $# == 2 ]]; then
      cat "$2"
    else
      key=$(cat .fixture-state-key)
      if test -s "$STORE/$key"; then cat "$STORE/$key"; else echo '{"values":{"root_module":{"resources":[]}}}'; fi
    fi ;;
  apply)
    key=$(cat .fixture-state-key)
    test -s "$STORE/${key%/*}/lease.json"
    plan=${*: -1}
    if [[ "$(jq -r '.resource_changes[0].change.actions[0]' "$plan")" == create ]]; then
      printf 'apply create\n' >> "$TRACE"
      jq '{values:{root_module:{resources:[.resource_changes[]|{address,type,mode,values:.change.before}]}}}' "$plan" > "$STORE/$key"
      if [[ ${ORPHAN_ENDPOINT:-} == true ]]; then
        jq -r '.resource_changes[0].change.before.tags_all.publisher_attempt' "$plan" > "$STORE/orphan-endpoint"
        jq 'del(.values.root_module.resources[]|select(.address=="aws_ec2_instance_connect_endpoint.builder"))' "$STORE/$key" > "$STORE/$key.tmp"
        mv "$STORE/$key.tmp" "$STORE/$key"
      fi
      [[ ${PARTIAL_APPLY:-} != true ]]
    else
      printf 'apply delete\n' >> "$TRACE"
      echo '{"values":{"root_module":{"resources":[]}}}' > "$STORE/$key"
      rm -f "$STORE/orphan-endpoint"
    fi ;;
  import)
    key=$(cat .fixture-state-key)
    [[ "$*" == *'aws_ec2_instance_connect_endpoint.builder eice-00000000000000003' ]]
    attempt=$(cat "$STORE/orphan-endpoint")
    jq --arg attempt "$attempt" --arg tag "$RELEASE_TAG" '.values.root_module.resources += [{
      address:"aws_ec2_instance_connect_endpoint.builder",type:"aws_ec2_instance_connect_endpoint",mode:"managed",
      values:{id:"eice-00000000000000003",tags_all:{project:"shiftbox",cluster_name:"shiftbox-publisher",
        publisher_attempt:$attempt,environment:("release-"+$tag)}}}]' "$STORE/$key" > "$STORE/$key.tmp"
    mv "$STORE/$key.tmp" "$STORE/$key"
    printf 'import failed endpoint\n' >> "$TRACE"
    ;;
  output)
    key=$(cat .fixture-state-key)
    lease="$STORE/${key%/*}/lease.json"
    public_key=$(jq -er '.variables.packer_public_key|split(" ")[1]' "$lease")
    hash=$(printf %s "$public_key" | shasum -a 256 | awk '{print $1}')
    jq -c --arg hash "$hash" '{
      account:.publisher_account_id, region:"us-east-2", attempt_id,candidate_tag,deadline:.expires_at,
      packer_key_pair:("shiftbox-publisher-"+.attempt_id+"-packer"),packer_public_key_sha256:$hash,
      builder_group_id:"sg-00000000000000004",subnet_id:"subnet-00000000000000002",
      private_subnet_id:"subnet-00000000000000002",
      private_subnet_cidr:(.variables.vpc_cidr | sub("[.]0/24$"; ".64/26")),
      vpc_id:"vpc-00000000000000001",endpoint_id:"eice-00000000000000003"
    }' "$lease" ;;
  force-unlock)
    printf 'unlock\n' >> "$TRACE"
    key=$(cat .fixture-state-key)
    test -f "$STORE/$key.tflock"
    rm -f -- "$STORE/$key.tflock" ;;
  *) exit 2 ;;
esac
STUB
chmod +x "$scratch/bin/"*
: > "$TRACE"
run() { bash "$here/release-network.sh" "$@"; }
reject() {
  if "$@" > "$scratch/rejected.log" 2>&1; then
    printf 'An invalid release network operation succeeded.\n' >&2
    exit 1
  fi
}

# Validate the AWS publisher, protected GitHub context, and bucket controls before
# writing a lease or asking Terraform to create anything.
reject env AWS_TEST_ACCOUNT=888293065132 bash "$here/release-network.sh" provision
reject env AWS_TEST_ROLE=unrelated bash "$here/release-network.sh" provision
reject env PUBLIC_BACKEND=true bash "$here/release-network.sh" provision
reject env OVERLAPS=true bash "$here/release-network.sh" provision
reject env GITHUB_REF_PROTECTED=false bash "$here/release-network.sh" provision
test ! -s "$TRACE"

# Persist only the public key and exact release identity before applying a saved
# plan, with the private key available only in the current runner directory.
run provision
lease="$STORE/release-builds/$GITHUB_RUN_ID/$GITHUB_RUN_ATTEMPT/lease.json"
jq -e --arg sha "$SOURCE_COMMIT" --arg tag "$RELEASE_TAG" '
  .schema == 1 and .source_commit == $sha and .candidate_tag == $tag and
  .job_name == "Build the private Ohio AMI" and .run_id == "10001" and .run_attempt == "1" and
  (.attempt_id|test("^rel-[0-9a-f]{14}$")) and
  (.variables.packer_public_key|startswith("ssh-ed25519 "))
' "$lease" >/dev/null
test -f "$RUNNER_TEMP/shiftbox-publisher-network/packer"
test -f "$RUNNER_TEMP/shiftbox-publisher-network/network.json"
if grep -q 'PRIVATE KEY' "$lease"; then exit 1; fi
test "$(head -n 1 "$TRACE")" = 'put lease.json'
test "$(grep -c 'apply create' "$TRACE")" = 1

# The same attempt cannot create a second network or retire a different run,
# and any remaining builder instance prevents destructive Terraform work.
reject run provision
reject run retire 10002 1
reject env ACTIVE_INSTANCE=true bash "$here/release-network.sh" retire 10001 1
if grep -q 'apply delete' "$TRACE"; then exit 1; fi

# Successful cleanup writes the encrypted retirement marker only after the
# network inventory is empty. The same cleanup can be retried.
reject env LEFTOVER_NAT=true bash "$here/release-network.sh" retire 10001 1
test ! -e "$STORE/release-builds/10001/1/retired.json"
run retire 10001 1
run retire 10001 1
test -f "$STORE/release-builds/10001/1/retired.json"
test ! -e "$RUNNER_TEMP/shiftbox-publisher-network/packer"
test "$(grep -c 'apply delete' "$TRACE")" = 1

# A partial apply leaves durable state and lease data, clears the ephemeral
# SSH key, and allows the same attempt to destroy its tracked resources.
export GITHUB_RUN_ATTEMPT=2 RUNNER_TEMP="$scratch/retry-runner"
mkdir "$RUNNER_TEMP"
reject env PARTIAL_APPLY=true bash "$here/release-network.sh" provision
test -f "$STORE/release-builds/10001/2/terraform.tfstate"
test ! -e "$RUNNER_TEMP/shiftbox-publisher-network/packer"
reject env FOREIGN_PLAN=true bash "$here/release-network.sh" retire 10001 2
test "$(grep -c 'apply delete' "$TRACE")" = 1
run retire 10001 2
test "$(grep -c 'apply delete' "$TRACE")" = 2
# An interrupted runner can leave an S3 lock. The same job cannot break it,
# and recovery ignores a lock for another state path and a fresh recovery lock.
export GITHUB_RUN_ATTEMPT=3 RUNNER_TEMP="$scratch/interrupted-runner"
mkdir "$RUNNER_TEMP"
reject env PARTIAL_APPLY=true bash "$here/release-network.sh" provision
lease="$STORE/release-builds/10001/3/lease.json"
lock="$STORE/release-builds/10001/3/terraform.tfstate.tflock"
jq '.expires_at="2020-02-01T00:00:00Z" | .variables.expires_at=.expires_at' "$lease" > "$lease.tmp"
mv "$lease.tmp" "$lease"
write_lock() {
  jq -n --arg path "$1" --arg created "$2" '{
    ID:"181292c7-3b8b-2ba2-a594-4387a92ed4c9", Operation:"OperationTypeApply", Path:$path, Created:$created
  }' > "$lock"
}
write_lock 'unrelated/path' '2020-01-01T00:00:00.123Z'
reject run retire 10001 3
if grep -q '^unlock$' "$TRACE"; then exit 1; fi
export RUNNER_TEMP="$scratch/recovery-runner" SHIFTBOX_RELEASE_RECOVERY=true
mkdir "$RUNNER_TEMP"
export GITHUB_REF=refs/heads/main GITHUB_EVENT_NAME=schedule
export GITHUB_WORKFLOW_REF=jflatten/ShiftBox/.github/workflows/cleanup-release-network.yml@refs/heads/main
export GITHUB_RUN_ID=22001 GITHUB_RUN_ATTEMPT=1
reject run retire 10001 3
if grep -q '^unlock$' "$TRACE"; then exit 1; fi
state_path="$SHIFTBOX_RELEASE_STATE_BUCKET/release-builds/10001/3/terraform.tfstate"
write_lock "$state_path" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
reject run retire 10001 3
if grep -q '^unlock$' "$TRACE"; then exit 1; fi
write_lock "$state_path" '2020-01-01T00:00:00.123Z'
run retire 10001 3
test "$(grep -c '^unlock$' "$TRACE")" = 1
test -f "$STORE/release-builds/10001/3/retired.json"

# A lost recovery runner is also recoverable after its own job timeout and grace.
export SHIFTBOX_RELEASE_RECOVERY=false GITHUB_REF="refs/tags/$RELEASE_TAG" GITHUB_EVENT_NAME=push
export GITHUB_RUN_ID=10001 GITHUB_RUN_ATTEMPT=4 RUNNER_TEMP="$scratch/interrupted-recovery-runner"
mkdir "$RUNNER_TEMP"
reject env PARTIAL_APPLY=true bash "$here/release-network.sh" provision
lease="$STORE/release-builds/10001/4/lease.json"
lock="$STORE/release-builds/10001/4/terraform.tfstate.tflock"
jq '.expires_at="2020-02-01T00:00:00Z" | .variables.expires_at=.expires_at' "$lease" > "$lease.tmp"
mv "$lease.tmp" "$lease"
write_lock "$SHIFTBOX_RELEASE_STATE_BUCKET/release-builds/10001/4/terraform.tfstate" '2020-02-02T00:00:00Z'
export SHIFTBOX_RELEASE_RECOVERY=true GITHUB_REF=refs/heads/main GITHUB_EVENT_NAME=schedule
export GITHUB_RUN_ID=22002 GITHUB_RUN_ATTEMPT=1 RUNNER_TEMP="$scratch/next-recovery-runner"
mkdir "$RUNNER_TEMP"
run retire 10001 4
test "$(grep -c '^unlock$' "$TRACE")" = 2
test -f "$STORE/release-builds/10001/4/retired.json"

# A manual rehearsal uses the same plans and state lock in an isolated namespace.
# Only the protected main dispatch and the separate rehearsal AWS role are accepted.
export SHIFTBOX_RELEASE_RECOVERY=false SHIFTBOX_RELEASE_NETWORK_REHEARSAL=true
export RELEASE_TAG=v0.0.0-rc.1 GITHUB_RUN_ID=10003 GITHUB_RUN_ATTEMPT=1
export GITHUB_REF=refs/heads/main GITHUB_EVENT_NAME=workflow_dispatch GITHUB_ACTOR_ID=11561330
export GITHUB_WORKFLOW_REF=jflatten/ShiftBox/.github/workflows/rehearse-release-network.yml@refs/heads/main
export RUNNER_TEMP="$scratch/network-rehearsal-runner"
mkdir "$RUNNER_TEMP"
reject env GITHUB_REF_PROTECTED=false bash "$here/release-network.sh" provision
reject env GITHUB_TRIGGERING_ACTOR=other bash "$here/release-network.sh" provision
reject env GITHUB_EVENT_NAME=push bash "$here/release-network.sh" provision
reject env GITHUB_WORKFLOW_REF=unrelated bash "$here/release-network.sh" provision
reject env AWS_TEST_ROLE=shiftbox-publisher-release-network bash "$here/release-network.sh" provision
reject env RELEASE_TAG=v2.4.0-rc.2 bash "$here/release-network.sh" provision
run provision
rehearsal="$STORE/release-builds/rehearsals/10003/1"
jq -e '.job_name=="Rehearse the private Ohio network" and .candidate_tag=="v0.0.0-rc.1" and
  .variables.vpc_cidr=="10.98.1.0/24" and (.attempt_id|test("^rel-r[0-9a-f]{13}$"))' "$rehearsal/lease.json" >/dev/null
test ! -e "$STORE/release-builds/10003/1"
run retire 10003 1
jq -e '.run_id=="10003" and (.attempt_id|startswith("rel-r"))' "$rehearsal/retired.json" >/dev/null
test ! -e "$RUNNER_TEMP/shiftbox-publisher-network/packer"

# An AWS endpoint record left behind by a failed create enters Terraform state
# only if it belongs to the original attempt's recorded VPC and private subnet.
export GITHUB_RUN_ID=10004 RUNNER_TEMP="$scratch/orphan-runner"
mkdir "$RUNNER_TEMP"
reject env PARTIAL_APPLY=true ORPHAN_ENDPOINT=true bash "$here/release-network.sh" provision
reject env ORPHAN_VPC=vpc-00000000000000009 bash "$here/release-network.sh" retire 10004 1
test -f "$STORE/orphan-endpoint"
if grep -q '^import failed endpoint$' "$TRACE"; then exit 1; fi
run retire 10004 1
test "$(grep -c '^import failed endpoint$' "$TRACE")" = 1
test ! -e "$STORE/orphan-endpoint"
test -f "$STORE/release-builds/rehearsals/10004/1/retired.json"
printf 'Release network lifecycle fixtures passed.\n'
