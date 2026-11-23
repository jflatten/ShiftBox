#!/usr/bin/env bash
# Apply and retire one publisher network using a private, locked S3 state key.
set -euo pipefail
umask 077
export AWS_REGION=us-east-2 AWS_DEFAULT_REGION=us-east-2 AWS_DEFAULT_OUTPUT=json AWS_PAGER=''
export AWS_CLI_AUTO_PROMPT=off AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
export TF_IN_AUTOMATION=1 TF_WORKSPACE=default

fail() { printf 'Release network: %s\n' "$1" >&2; exit 1; }
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=platform/packer/aws/terraform-workflow.sh
source "$here/terraform-workflow.sh"
repo=$(cd -- "$here/../../.." && pwd)
cd "$repo"

operation=${1:-}
account=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
bucket=${SHIFTBOX_RELEASE_STATE_BUCKET:-}
tag=${RELEASE_TAG:-}
source_commit=${SOURCE_COMMIT:-}
cidr=10.98.0.0/24
job='Build the private Ohio AMI'
namespace=release-builds
rehearsal=${SHIFTBOX_RELEASE_NETWORK_REHEARSAL:-false}
tf_rel=platform/terraform/publisher/ohio-build
tag_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'
[[ "$account" =~ ^[0-9]{12}$ && "$account" != 524941137496 && "$account" != 888293065132 &&
   "$bucket" == "shiftbox-release-state-$account-us-east-2" ]] || fail 'use the dedicated production state bucket'
[[ "$tag" =~ $tag_pattern && ${#tag} -le 96 && "$source_commit" =~ ^[0-9a-f]{40}$ ]] ||
    fail 'provide the exact protected RC tag and source'
if [[ "$rehearsal" == true ]]; then
    [[ "$tag" == v0.0.0-rc.1 ]] || fail 'use the reserved rehearsal resource label'
    job='Rehearse the private Ohio network'
    namespace=release-builds/rehearsals
    cidr=10.98.1.0/24
fi
[[ "${GITHUB_REPOSITORY:-}" == jflatten/ShiftBox && "${GITHUB_REPOSITORY_ID:-}" == 1457179716 &&
   "${GITHUB_REPOSITORY_OWNER_ID:-}" == 11561330 ]] || fail 'use the verified publisher repository'
[[ -d "${RUNNER_TEMP:-}" && "$RUNNER_TEMP" == /* ]] || fail 'use a private runner temporary directory'

case "$operation:$#" in
  provision:1)
    run=${GITHUB_RUN_ID:-} retry=${GITHUB_RUN_ATTEMPT:-}
    ;;
  retire:3)
    run=$2 retry=$3
    ;;
  *)
    fail 'use provision or retire <run-id> <run-attempt>'
    ;;
esac
[[ "$run" =~ ^[1-9][0-9]{0,19}$ && "$retry" =~ ^[1-9][0-9]{0,5}$ ]] || fail 'invalid workflow attempt identity'

if [[ "$operation" == provision || "${SHIFTBOX_RELEASE_RECOVERY:-}" != true ]]; then
    [[ "$run" == "${GITHUB_RUN_ID:-}" && "$retry" == "${GITHUB_RUN_ATTEMPT:-}" &&
       "${GITHUB_SHA:-}" == "$source_commit" && "$(git rev-parse HEAD)" == "$source_commit" ]] ||
        fail 'the active release cannot operate on another workflow attempt'
    if [[ "$rehearsal" == true ]]; then
        [[ "${GITHUB_EVENT_NAME:-}" == workflow_dispatch && "${GITHUB_REF:-}" == refs/heads/main &&
           "${GITHUB_REF_PROTECTED:-}" == true && "$retry" == 1 &&
           "${GITHUB_ACTOR_ID:-}" == 11561330 && "${GITHUB_TRIGGERING_ACTOR:-}" == jflatten &&
           "${GITHUB_WORKFLOW_REF:-}" == jflatten/ShiftBox/.github/workflows/rehearse-release-network.yml@refs/heads/main ]] ||
            fail 'the rehearsal must be a maintainer dispatch on protected main'
        expected_session="shiftbox-publisher-release-network-rehearsal/shiftbox-release-rehearsal-$run-$retry"
    else
        [[ "${GITHUB_REF:-}" == "refs/tags/$tag" &&
           "$("$repo/platform/release/publish.sh" check-event)" == rc &&
           "$("$repo/platform/release/publish.sh" check-source "$tag" "$source_commit")" == "$source_commit" ]] ||
            fail 'the current checkout is outside the protected RC'
        expected_session="shiftbox-publisher-release-network/shiftbox-release-network-$run-$retry"
    fi
else
    [[ "${GITHUB_REF:-}" == refs/heads/main &&
       "${GITHUB_REF_PROTECTED:-}" == true &&
       "${GITHUB_WORKFLOW_REF:-}" == jflatten/ShiftBox/.github/workflows/cleanup-release-network.yml@refs/heads/main &&
       "${GITHUB_RUN_ID:-}" =~ ^[1-9][0-9]{0,19}$ && "${GITHUB_RUN_ATTEMPT:-}" =~ ^[1-9][0-9]{0,5}$ &&
       "${GITHUB_SHA:-}" == "$(git rev-parse HEAD)" &&
       ( "${GITHUB_EVENT_NAME:-}" == schedule || "${GITHUB_EVENT_NAME:-}" == workflow_dispatch ) ]] ||
        fail 'independent recovery must run from protected main'
    [[ "$(git rev-parse --verify "$source_commit^{commit}")" == "$source_commit" ]] ||
        fail 'the recovery source is not an exact commit'
    git merge-base --is-ancestor "$source_commit" HEAD || fail 'the recovery source is outside reviewed main'
    expected_session="shiftbox-publisher-release-network-cleanup/shiftbox-release-cleanup-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
fi
[[ "$(aws sts get-caller-identity --query Account --output text)" == "$account" ]] ||
    fail 'AWS caller is outside the production publisher account'
[[ "$(aws sts get-caller-identity --query Arn --output text)" == "arn:aws:sts::$account:assumed-role/$expected_session" ]] ||
    fail 'AWS caller is outside the workflow attempt session'

attempt_hash=$(printf %s "$run/$retry" | shasum -a 256 | cut -c1-14)
attempt="rel-$attempt_hash"
if [[ "$rehearsal" == true ]]; then attempt="rel-r${attempt_hash:0:13}"; fi
prefix="$namespace/$run/$retry"
state_key="$prefix/terraform.tfstate"
lease_key="$prefix/lease.json"
retired_key="$prefix/retired.json"
boundary="arn:aws:iam::$account:policy/shiftbox-publisher-release-expiry-boundary"
output_dir="$RUNNER_TEMP/shiftbox-publisher-network"
scratch=$(mktemp -d "$RUNNER_TEMP/release-network.XXXXXX")
tf_root="$scratch/$tf_rel"
lease="$scratch/lease.json"
inputs="$scratch/network.tfvars.json"
handoff=false
cleanup() {
    rm -rf -- "$scratch"
    if [[ "$operation" == provision && "$handoff" != true && -n "${private_key:-}" ]]; then
        rm -f -- "$private_key" "$private_key.pub"
    fi
}
trap cleanup EXIT

# The backend is pre-provisioned independently of this disposable network.
verify_bucket() {
    aws s3api get-bucket-location --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.LocationConstraint=="us-east-2"' >/dev/null &&
    aws s3api get-public-access-block --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.PublicAccessBlockConfiguration=={BlockPublicAcls:true,IgnorePublicAcls:true,BlockPublicPolicy:true,RestrictPublicBuckets:true}' >/dev/null &&
    aws s3api get-bucket-policy-status --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.PolicyStatus.IsPublic==false' >/dev/null &&
    aws s3api get-bucket-ownership-controls --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.OwnershipControls.Rules==[{ObjectOwnership:"BucketOwnerEnforced"}]' >/dev/null &&
    aws s3api get-bucket-versioning --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.Status=="Enabled"' >/dev/null &&
    aws s3api get-bucket-encryption --bucket "$bucket" --expected-bucket-owner "$account" |
        jq -e '.ServerSideEncryptionConfiguration.Rules | length==1 and .[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm=="AES256"' >/dev/null
}
object_exists() {
    local key=$1 objects
    objects=$(aws s3api list-objects-v2 --bucket "$bucket" --expected-bucket-owner "$account" --prefix "$key") ||
        fail 'could not inspect the exact state prefix'
    jq -e '(.Contents // []) | type=="array" and all(.[]; .Key|type=="string")' <<< "$objects" >/dev/null ||
        fail 'invalid state object listing'
    jq -e --arg key "$key" 'any(.Contents[]?; .Key==$key)' <<< "$objects" >/dev/null
}
get_object() {
    aws s3api get-object --bucket "$bucket" --expected-bucket-owner "$account" --key "$1" "$2" |
        jq -e '.ServerSideEncryption=="AES256"' >/dev/null
}
create_object() {
    aws s3api put-object --bucket "$bucket" --expected-bucket-owner "$account" --key "$1" --body "$2" \
        --if-none-match '*' --server-side-encryption AES256 --content-type application/json |
        jq -e '.ServerSideEncryption=="AES256" and (.VersionId | type=="string" and length>0 and .!="null")' >/dev/null
}
verified_lease() {
    jq -e --arg run "$run" --arg retry "$retry" --arg tag "$tag" --arg sha "$source_commit" \
        --arg attempt "$attempt" --arg account "$account" --arg boundary "$boundary" --arg cidr "$cidr" --arg job "$job" '
      .schema==1 and .run_id==$run and .run_attempt==$retry and .candidate_tag==$tag and .source_commit==$sha and
      .attempt_id==$attempt and .publisher_account_id==$account and .job_name==$job and
      (.source_ami | test("^ami-[0-9a-f]{17}$")) and (.source_snapshot | test("^snap-[0-9a-f]{17}$")) and
      (.expires_at | fromdateiso8601>0) and (.variables | keys==[
        "allow_builder_http","attempt_id","availability_zone_id","candidate_tag","expires_at",
        "expiry_permissions_boundary_arn","packer_public_key","publisher_account_id","vpc_cidr"]) and
      (.variables.expires_at==.expires_at) and
      (.variables|.publisher_account_id==$account and .candidate_tag==$tag and .attempt_id==$attempt and
        .allow_builder_http==false and .vpc_cidr==$cidr and .expiry_permissions_boundary_arn==$boundary and
        (.availability_zone_id | IN("use2-az1","use2-az2","use2-az3")) and
        (.packer_public_key | test("^ssh-ed25519 [A-Za-z0-9+/=]+( [^\r\n]+)?$")))
    ' "$lease" >/dev/null
}
init_terraform() {
    git archive "$source_commit" "$tf_rel" | tar -xf - -C "$scratch"
    (cd "$tf_root" && zip -q -X expiry.zip expiry.py)
    printf 'terraform {\n  backend "s3" {}\n}\n' > "$tf_root/terraform-s3.tf"
    cat > "$scratch/backend.hcl" <<BACKEND
bucket               = "$bucket"
key                  = "$state_key"
region               = "us-east-2"
workspace_key_prefix = "$namespace"
allowed_account_ids  = ["$account"]
encrypt              = true
use_lockfile         = true
BACKEND
    tf_logged init -input=false -lockfile=readonly -backend-config="$scratch/backend.hcl" ||
        fail 'could not initialize the leased Terraform backend'
}
tf() { terraform -chdir="$tf_root" "$@"; }

# Audit both the plan and any interrupted state against the network root's
# resource inventory. Nothing in this set is a release artifact or bucket.
network_addresses() {
    cat <<'ADDRESSES'
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
}

finalize_handoff() {
    local pin
    pin=$(cut -d ' ' -f 2 < "$private_key.pub" | tr -d '\n' | shasum -a 256 | cut -c1-64)
    tf output -json attempt > "$scratch/outputs.json"
    jq -e --arg pin "$pin" --arg ami "$ami" --arg private_cidr "${cidr%.0/24}.64/26" --slurpfile lease "$lease" '
      $lease[0] as $l |
      select(.account==$l.publisher_account_id and .region=="us-east-2" and .candidate_tag==$l.candidate_tag and
        .attempt_id==$l.attempt_id and .deadline==$l.expires_at and
        ((.deadline | fromdateiso8601)-now >= 3000) and .packer_public_key_sha256==$pin and
        .packer_key_pair==("shiftbox-publisher-"+$l.attempt_id+"-packer") and .private_subnet_cidr==$private_cidr and
        (.vpc_id|test("^vpc-[0-9a-f]{17}$")) and (.private_subnet_id|test("^subnet-[0-9a-f]{17}$")) and
        (.builder_group_id|test("^sg-[0-9a-f]{17}$")) and (.endpoint_id|test("^eice-[0-9a-f]{17}$"))) |
      {account,attempt_id,builder_group_id,candidate_tag,deadline,endpoint_id,packer_key_pair,packer_public_key_sha256,
        private_subnet_cidr,private_subnet_id,region,source_ami:$ami,vpc_id}
    ' "$scratch/outputs.json" > "$output_dir/network.json" || fail 'Terraform produced an unexpected or expired network handoff'
    cp "$lease" "$output_dir/lease.json"
    handoff=true
}
provision() {
    ami=${SHIFTBOX_RELEASE_SOURCE_AMI:-}
    [[ "$ami" =~ ^ami-[0-9a-f]{17}$ ]] || fail 'provide the verified Canonical source AMI pin'
    if object_exists "$lease_key" || object_exists "$state_key" || object_exists "$state_key.tflock"; then
        fail 'this workflow attempt already owns durable state'
    fi
    aws ec2 describe-vpcs > "$scratch/vpcs.json"
    python3 - "$scratch/vpcs.json" "$cidr" <<'PY' || fail 'the Ohio build network overlaps an existing VPC'
import ipaddress
import json
import sys

with open(sys.argv[1]) as inventory:
    vpcs = json.load(inventory)["Vpcs"]
proposed = ipaddress.ip_network(sys.argv[2])
for vpc in vpcs:
    prefixes = [vpc["CidrBlock"]] + [association["CidrBlock"] for association in vpc.get("CidrBlockAssociationSet", [])]
    if any(proposed.overlaps(ipaddress.ip_network(prefix)) for prefix in prefixes):
        sys.exit(1)
PY
    aws ec2 describe-availability-zones --zone-ids use2-az1 use2-az2 use2-az3 > "$scratch/zones.json"
    aws ec2 describe-instance-type-offerings --location-type availability-zone-id \
        --filters Name=instance-type,Values=t3.medium > "$scratch/offerings.json"
    zone=$(jq -er --slurpfile offerings "$scratch/offerings.json" '
      ($offerings[0].InstanceTypeOfferings | map(select(.InstanceType=="t3.medium")|.Location)) as $offered |
      [.AvailabilityZones[] | select(.State=="available") | .ZoneId |
        select(IN("use2-az1","use2-az2","use2-az3")) | select(IN($offered[]))] | sort | .[0] | strings
    ' "$scratch/zones.json") || fail 'no available Ohio zone offers the private builder type'
    aws ec2 describe-images --image-ids "$ami" --owners 099720109477 > "$scratch/source.json"
    snapshot=$(jq -er --arg ami "$ami" '
      .Images | select(length==1) | .[0] | select(.ImageId==$ami and .OwnerId=="099720109477" and .State=="available" and
        .Public==true and .Architecture=="x86_64" and .RootDeviceType=="ebs" and .VirtualizationType=="hvm" and
        .RootDeviceName=="/dev/sda1") |
      [.BlockDeviceMappings[]|select(.Ebs!=null)] | select(length==1) | .[0] |
      select(.DeviceName=="/dev/sda1" and .Ebs.Encrypted==false and (.Ebs.VolumeSize|type=="number" and .>=1 and .<=32)) |
      .Ebs.SnapshotId | select(test("^snap-[0-9a-f]{17}$"))
    ' "$scratch/source.json") || fail 'unexpected source owner or root image'
    mkdir -m 700 "$output_dir" || fail 'the runner already contains a network handoff'
    private_key="$output_dir/packer"
    ssh-keygen -q -t ed25519 -N '' -C "shiftbox-$attempt" -f "$private_key"
    expires=$(python3 -c 'from datetime import datetime, timedelta, timezone; print((datetime.now(timezone.utc)+timedelta(minutes=119)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
    jq -n --arg publisher "$account" --arg cidr "$cidr" --arg attempt "$attempt" --arg tag "$tag" \
        --arg zone "$zone" --arg expires "$expires" --arg key "$(cat "$private_key.pub")" --arg boundary "$boundary" '
      {publisher_account_id:$publisher,vpc_cidr:$cidr,attempt_id:$attempt,candidate_tag:$tag,availability_zone_id:$zone,
        packer_public_key:$key,expires_at:$expires,allow_builder_http:false,expiry_permissions_boundary_arn:$boundary}
    ' > "$inputs"
    jq -n --slurpfile variables "$inputs" --arg run "$run" --arg retry "$retry" --arg sha "$source_commit" \
        --arg job "$job" --arg ami "$ami" --arg snapshot "$snapshot" '
      $variables[0] as $v | {schema:1, publisher_account_id:$v.publisher_account_id, candidate_tag:$v.candidate_tag,
        source_commit:$sha,run_id:$run,run_attempt:$retry,attempt_id:$v.attempt_id,expires_at:$v.expires_at,job_name:$job,
        source_ami:$ami,source_snapshot:$snapshot,variables:$v}
    ' > "$lease"
    verified_lease || fail 'could not verify the durable release lease'
    create_object "$lease_key" "$lease" || fail 'could not reserve and persist the exact workflow attempt'
    init_terraform
    tf_logged plan -input=false -lock-timeout=60s -var-file="$inputs" -out="$scratch/create.tfplan" ||
        fail 'Terraform could not plan the network'
    tf show -json "$scratch/create.tfplan" > "$scratch/plan.json"
    jq -e --rawfile expected "$scratch/addresses" --slurpfile vars "$inputs" '
      (.variables | map_values(.value))==$vars[0] and
      (.resource_changes | map(.address)|sort)==($expected|split("\n")|map(select(length>0))|sort) and
      all(.resource_changes[]; .mode=="managed" and .change.actions==["create"])
    ' "$scratch/plan.json" >/dev/null || fail 'the saved plan would change resources outside this network'
    tf_logged apply -input=false -lock-timeout=60s "$scratch/create.tfplan" ||
        fail 'network application failed, retain the lease for recovery'
    finalize_handoff
    printf 'Verified the private Ohio build network handoff.\n'
}

retired_identity() {
    jq -c '{schema,run_id,run_attempt,candidate_tag,source_commit,attempt_id,publisher_account_id}' "$lease"
}
unlock_expired_state() {
    local id
    object_exists "$state_key.tflock" || return 0
    [[ "${SHIFTBOX_RELEASE_RECOVERY:-}" == true ]] || fail 'the network state is still locked'
    # The protected recovery job has already checked the original build is
    # terminal. A later recovery lock gets its own 25-minute timeout and grace.
    get_object "$state_key.tflock" "$scratch/lock.json"
    id=$(jq -er --arg path "$bucket/$state_key" --slurpfile lease "$lease" '
      def epoch: sub("[.][0-9]+Z$";"Z")|fromdateiso8601;
      ($lease[0].expires_at|epoch) as $expiry | (.Created|epoch) as $created |
      select($expiry<=now and .Path==$path and ($created<$expiry or $created<=now-1800) and
        (.Operation | IN("OperationTypePlan","OperationTypeApply")) and
        (.ID|test("^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$"))) | .ID
    ' "$scratch/lock.json") || fail 'refusing to unlock a new, unrelated, or still-leased state lock'
    tf_logged force-unlock -force "$id" || fail 'failed to remove the expired Terraform lock'
}
check_build_stopped() {
    local filter="Name=tag:publisher_attempt,Values=$attempt"
    if [[ -n "${vpc:-}" ]]; then filter="Name=vpc-id,Values=$vpc"; fi
    aws ec2 describe-instances --filters "$filter" \
        Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down |
        jq -e '.Reservations==[]' >/dev/null &&
    aws ec2 describe-volumes --filters "Name=tag:publisher_attempt,Values=$attempt" |
        jq -e '.Volumes==[]' >/dev/null
}
verify_no_network() {
    local filter="Name=tag:publisher_attempt,Values=$attempt"
    aws ec2 describe-vpcs --filters "$filter" | jq -e '.Vpcs==[]' >/dev/null &&
    aws ec2 describe-nat-gateways --filter "$filter" | jq -e 'all(.NatGateways[];.State=="deleted")' >/dev/null &&
    aws ec2 describe-addresses --filters "$filter" | jq -e '.Addresses==[]' >/dev/null &&
    aws ec2 describe-instance-connect-endpoints --filters "$filter" |
        jq -e 'all(.InstanceConnectEndpoints[];.State=="delete-complete")' >/dev/null &&
    aws ec2 describe-key-pairs --filters "$filter" | jq -e '.KeyPairs==[]' >/dev/null &&
    aws ec2 describe-network-interfaces --filters "$filter" | jq -e '.NetworkInterfaces==[]' >/dev/null
}

verify_attempt_state() {
    jq -e --rawfile expected "$scratch/addresses" --arg attempt "$attempt" --arg tag "$tag" '
      ($expected|split("\n")) as $expected |
      (.values.root_module // {}) | (.child_modules // [])==[] and
      all(.resources[]?; .address as $address | .mode=="managed" and ($expected|index($address)!=null) and
        (.values.id|type=="string" and length>0) and
        (.values.tags_all==null or .values.tags_all=={} or
         (.values.tags_all|.project=="shiftbox" and .cluster_name=="shiftbox-publisher" and
         .publisher_attempt==$attempt and .environment==("release-"+$tag))))
    ' "$scratch/prior.json" >/dev/null || fail 'the state contains resources outside the exact attempt'
}

state_id() {
    jq -r --arg address "$1" '
      [.values.root_module.resources[]?|select(.address==$address)|.values.id] | first // ""
    ' "$scratch/prior.json"
}

adopt_failed_endpoint() {
    local endpoint_address=aws_ec2_instance_connect_endpoint.builder tracked subnet endpoint
    tracked=$(state_id "$endpoint_address")
    subnet=$(state_id aws_subnet.private)
    # EC2 can return an endpoint ID before its interface fails asynchronously.
    # Adopt only that attempt's sole untracked endpoint, under its saved parents.
    endpoint=$(aws ec2 describe-instance-connect-endpoints --filters "Name=tag:publisher_attempt,Values=$attempt" |
      jq -er --arg tracked "$tracked" --arg vpc "$vpc" --arg subnet "$subnet" --arg attempt "$attempt" --arg tag "$tag" '
        .InstanceConnectEndpoints | select(type=="array") |
        map(select(.State!="delete-complete" and .InstanceConnectEndpointId!=$tracked)) |
        if length==0 then ""
        elif length==1 and $tracked=="" and ($vpc|test("^vpc-[0-9a-f]{17}$")) and ($subnet|test("^subnet-[0-9a-f]{17}$")) then
          .[0] | select(.VpcId==$vpc and .SubnetId==$subnet and (.State|IN("create-failed","create-complete")) and
            ([.Tags[]|{key:.Key,value:.Value}]|from_entries|.project=="shiftbox" and .cluster_name=="shiftbox-publisher" and
            .publisher_attempt==$attempt and .environment==("release-"+$tag))) |
          .InstanceConnectEndpointId | select(test("^eice-[0-9a-f]{17}$"))
        else empty end
      ') || fail 'could not prove ownership of the untracked endpoint'
    [[ -n "$endpoint" ]] || return 0
    tf_logged import -input=false -lock-timeout=60s -var-file="$inputs" "$endpoint_address" "$endpoint" ||
        fail 'could not adopt the owned endpoint for retirement'
    tf show -json > "$scratch/prior.json"
    verify_attempt_state
    [[ "$(state_id "$endpoint_address")" == "$endpoint" ]] || fail 'the adopted endpoint is outside the verified state'
}

retire() {
    if [[ "${SHIFTBOX_RELEASE_RECOVERY:-}" != true ]]; then
        # The temporary key is needed only during the hosted build.
        rm -f -- "$output_dir/packer" "$output_dir/packer.pub"
    fi
    if ! object_exists "$lease_key"; then
        if object_exists "$state_key" || object_exists "$state_key.tflock"; then
            fail 'state or lock exists without its protected lease'
        fi
        return 0
    fi
    get_object "$lease_key" "$lease" || fail 'could not read the protected lease'
    verified_lease || fail 'invalid durable lease, refusing to retire'
    if [[ "${SHIFTBOX_RELEASE_RECOVERY:-}" == true ]]; then
        jq -e '(.expires_at|fromdateiso8601)<=now' "$lease" >/dev/null || fail 'the original workflow still owns the active lease'
    fi
    if object_exists "$retired_key"; then
        get_object "$retired_key" "$scratch/retired.json" || fail 'could not read the retirement marker'
        jq -e --argjson identity "$(retired_identity)" '.==$identity' "$scratch/retired.json" >/dev/null ||
            fail 'retirement marker does not match the protected lease'
        return 0
    fi
    jq -ce .variables "$lease" > "$inputs"
    if object_exists "$state_key" || object_exists "$state_key.tflock"; then
        init_terraform
        unlock_expired_state
        if object_exists "$state_key"; then
            tf show -json > "$scratch/prior.json"
        else
            printf '{}\n' > "$scratch/prior.json"
        fi
        verify_attempt_state
        vpc=$(state_id aws_vpc.build)
        check_build_stopped || fail 'builder compute remains, keep the lease for independent cleanup'
        adopt_failed_endpoint
        if jq -e '(.values.root_module.resources // [])|length>0' "$scratch/prior.json" >/dev/null; then
            tf_logged plan -destroy -input=false -lock-timeout=60s -var-file="$inputs" -out="$scratch/destroy.tfplan" ||
                fail 'could not plan network retirement'
            tf show -json "$scratch/destroy.tfplan" > "$scratch/plan.json"
            jq -e --slurpfile prior "$scratch/prior.json" --slurpfile vars "$inputs" '
              (.variables | map_values(.value))==$vars[0] and
              all(.resource_changes[]; . as $change | .mode=="managed" and .change.actions==["delete"] and
                any($prior[0].values.root_module.resources[];
                  .address==$change.address and .type==$change.type and .values.id==$change.change.before.id))
            ' "$scratch/plan.json" >/dev/null || fail 'retirement plan is outside the verified network state'
            tf_logged apply -input=false -lock-timeout=60s "$scratch/destroy.tfplan" ||
                fail 'network retirement failed, retain the lease for recovery'
            tf show -json | jq -e '(.values.root_module.resources // [])==[]' >/dev/null ||
                fail 'network state still contains resources'
        fi
    fi
    check_build_stopped || fail 'builder compute remains, retain the lease for recovery'
    verify_no_network || fail 'network resources remain, retain the lease for recovery'
    retired_identity > "$scratch/retired.json"
    create_object "$retired_key" "$scratch/retired.json" || fail 'could not record the verified network retirement'
    printf 'Verified retirement of the exact publisher network.\n'
}

verify_bucket || fail 'the production Terraform backend lacks its required storage protections'
network_addresses > "$scratch/addresses"
"$operation"
