#!/bin/sh
set -eu
umask 077

if [ "$#" -ne 3 ]; then
    echo "Usage: build-image.sh <release-id> <guest-update-public-key> <deployment.pkrvars.hcl>" >&2
    exit 2
fi

release_id=$1
guest_agent_release=${GUEST_AGENT_RELEASE:-$release_id}
release_candidate=${RELEASE_CANDIDATE:-}
public_build=${PUBLIC_AMI_BUILD:-0}
publisher_account=''
canonical_owner=099720109477
publisher_key=$2
deployment_vars=$3
case "$public_build" in
    0|1) ;;
    *) echo 'PUBLIC_AMI_BUILD must be 0 or 1.' >&2; exit 2 ;;
esac
case "$release_id" in
    ''|*[!a-zA-Z0-9._-]*) echo "Release ID contains unsupported characters." >&2; exit 2 ;;
esac
if [ "${#release_id}" -gt 96 ]; then
    echo "Release ID exceeds 96 characters." >&2
    exit 2
fi
if [ -n "$release_candidate" ]; then
    if [ "$release_candidate" != "$release_id" ] || [ "$release_candidate" != "$guest_agent_release" ] ||
        ! printf '%s\n' "$release_id" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'; then
        echo 'The public AMI and guest must use the same canonical RELEASE_CANDIDATE tag.' >&2
        exit 2
    fi
fi

case "$publisher_key" in
    /*) ;;
    *) publisher_key="$PWD/$publisher_key" ;;
esac
case "$deployment_vars" in
    /*) ;;
    *) deployment_vars="$PWD/$deployment_vars" ;;
esac
if [ ! -f "$publisher_key" ] || [ "$(wc -c < "$publisher_key")" -ne 32 ]; then
    echo "The Ed25519 guest update public key must contain 32 raw bytes." >&2
    exit 2
fi
if [ ! -f "$deployment_vars" ]; then
    echo "The deployment Packer variables file is missing." >&2
    exit 2
fi

public_preflight() {
    if [ -z "$release_candidate" ]; then
        echo 'A public image requires RELEASE_CANDIDATE.' >&2
        return 2
    fi
    publisher_account=${RELEASE_PUBLISHER_ACCOUNT_ID:-}
    case "$publisher_account" in
        ''|*[!0-9]*) echo 'Set the approved publisher account ID.' >&2; return 2 ;;
    esac
    if [ "${#publisher_account}" -ne 12 ]; then
        echo 'Set the approved publisher account ID.' >&2
        return 2
    fi
    case "$deployment_vars" in
        *.pkrvars.json) ;;
        *) echo 'Public builds require a publisher .pkrvars.json file.' >&2; return 2 ;;
    esac
    region=$(jq -er '.region' "$deployment_vars")
    case "$region" in
        us-east-1|us-east-2|us-west-1|us-west-2) ;;
        *) echo 'Choose a supported public image Region.' >&2; return 2 ;;
    esac
    source_ami=$(jq -er '.source_ami | select(type == "string" and test("\\Aami-([a-f0-9]{8}|[a-f0-9]{17})\\z"))' "$deployment_vars")
    identity=$(aws sts get-caller-identity --region "$region" --query Account --output text)
    if [ "$identity" != "$publisher_account" ]; then
        echo 'The AWS session must belong to the approved publisher account.' >&2
        return 2
    fi
    encrypted=$(aws ec2 get-ebs-encryption-by-default --region "$region" --query EbsEncryptionByDefault --output text)
    case "$encrypted" in
        False|false) ;;
        *) echo 'The public image requires an explicitly unencrypted build root in the publisher Region.' >&2; return 2 ;;
    esac
    if ! image_snapshot "$source_ami" "$canonical_owner" true >/dev/null; then
        echo 'The source must be a pinned, unencrypted Canonical Ubuntu 24.04 Minimal amd64 AMI.' >&2
        return 2
    fi
}

image_snapshot() {
    images=$(aws ec2 describe-images --region "$region" --owners "$2" --image-ids "$1" --output json) || return
    printf '%s\n' "$images" | jq -er --arg ami "$1" --arg owner "$2" --argjson public "$3" \
        --arg release "$release_id" --arg source "$source_ami" --arg revision "$revision" '
        .Images | select(length == 1) | .[0] |
        select(.ImageId == $ami and .OwnerId == $owner and .Public == $public and .State == "available" and
               .Architecture == "x86_64" and .RootDeviceName == "/dev/sda1" and .RootDeviceType == "ebs" and
               .VirtualizationType == "hvm" and ((.ProductCodes // []) | length == 0)) |
        select(if $public then .Name | startswith("ubuntu-minimal/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-minimal-")
          else (.Tags | map({key:.Key,value:.Value}) | from_entries) as $tags |
               $tags.ShiftBoxArtifact == "workspace-ami" and $tags.ShiftBoxRelease == $release and
               $tags.ShiftBoxSourceAMI == $source and $tags.ShiftBoxSourceCommit == $revision end) |
        [.BlockDeviceMappings[] | select(.Ebs != null)] | select(length == 1) | .[0] |
        select(.DeviceName == "/dev/sda1" and .Ebs.Encrypted == false and ($public or .Ebs.VolumeSize == 32)) |
        .Ebs.SnapshotId | select(type == "string" and test("\\Asnap-([a-f0-9]{8}|[a-f0-9]{17})\\z"))'
}

verify_public_output() {
    ami=$(jq -er --arg region "$region" --arg source "$source_ami" --arg publisher "$publisher_account" \
        --arg revision "$revision" --arg release "$release_id" --arg agent "${agent_sha%% *}" \
        --arg updateKey "${publisher_key_sha%% *}" '
        .last_run_uuid as $run | .builds | select(length == 1) | .[0] |
        select(.builder_type == "amazon-ebs" and $run != null and .packer_run_uuid == $run) |
        .custom_data as $data |
        select($data.provider == "ec2" and $data.publicBuild == "true" and $data.region == $region and
               $data.sourceRegion == $region and $data.publisherAccountId == $publisher and $data.sourceAmiId == $source and
               $data.sourceAmiOwnerId == "099720109477" and $data.sourceRevision == $revision and
               $data.releaseId == $release and $data.guestAgentRelease == $release and
               $data.guestAgentSHA256 == $agent and $data.guestUpdateKeySHA256 == $updateKey and
               $data.architecture == "amd64" and $data.minimumRootDiskGiB == "32") |
        .artifact_id | select(startswith($region + ":")) | ltrimstr($region + ":") |
        select(test("\\Aami-([a-f0-9]{8}|[a-f0-9]{17})\\z"))' "$build_dir/manifest.json") || return
    snapshot=$(image_snapshot "$ami" "$publisher_account" false) || return
    metadata=$(aws ec2 describe-snapshots --region "$region" --owner-ids "$publisher_account" \
        --snapshot-ids "$snapshot" --output json) || return
    printf '%s\n' "$metadata" | jq -e --arg snapshot "$snapshot" --arg publisher "$publisher_account" '
        .Snapshots | length == 1 and all(.[]; .SnapshotId == $snapshot and .OwnerId == $publisher and
        .Encrypted == false and .State == "completed" and .VolumeSize == 32)' >/dev/null
}

recipe_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$recipe_dir/../../.." && pwd)
cd "$repo_dir"
revision=$(git rev-parse HEAD)
# Record when a validation build contains changes outside the recorded commit.
if [ -n "$release_candidate" ]; then
    source_status=$(git status --porcelain --untracked-files=normal)
    if [ -n "$source_status" ]; then
        echo 'A release candidate must be built from a clean checkout.' >&2
        exit 2
    fi
elif [ -n "$(git status --porcelain --untracked-files=normal -- cmd internal platform go.mod go.sum)" ]; then
    revision="$revision-dirty"
fi

public_flag=false
if [ "$public_build" -eq 1 ]; then
    export AWS_PAGER=''
    public_preflight
    public_flag=true
fi

mkdir -p .cache/packer
build_dir=$(mktemp -d "$repo_dir/.cache/packer/aws.XXXXXXXX")
cp "$publisher_key" "$build_dir/guest-update.pub"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "${GO:-go}" build -trimpath \
    -ldflags "-X main.agentRelease=$guest_agent_release" -o "$build_dir/shiftbox-agent" ./cmd/shiftbox-agent
agent_sha=$(shasum -a 256 < "$build_dir/shiftbox-agent")
publisher_key_sha=$(shasum -a 256 < "$build_dir/guest-update.pub")

"${PACKER:-packer}" build \
    -var-file="$deployment_vars" \
    -var "public_build=$public_flag" \
    -var "publisher_account_id=$publisher_account" \
    -var "release_id=$release_id" \
    -var "guest_agent_release=$guest_agent_release" \
    -var "source_revision=$revision" \
    -var "agent_binary=$build_dir/shiftbox-agent" \
    -var "agent_sha256=${agent_sha%% *}" \
    -var "guest_update_public_key=$build_dir/guest-update.pub" \
    -var "guest_update_key_sha256=${publisher_key_sha%% *}" \
    -var "manifest_path=$build_dir/manifest.json" \
    "$recipe_dir"

if [ "$public_build" -eq 1 ] && ! verify_public_output; then
    echo 'Public build metadata is unverified. Keep the AMI private and inspect the local Packer output.' >&2
    exit 1
fi

echo "AMI provenance manifest: $build_dir/manifest.json"
