#!/bin/sh
set -eu
umask 077

if [ "$#" -ne 3 ]; then
    echo "Usage: build-image.sh <release-id> <guest-update-public-key> <deployment.pkrvars.hcl>" >&2
    exit 2
fi

release_id=$1
guest_agent_release=${GUEST_AGENT_RELEASE:-$release_id}
publisher_key=$2
deployment_vars=$3
case "$release_id" in
    ''|*[!a-zA-Z0-9._-]*) echo "Release ID contains unsupported characters." >&2; exit 2 ;;
esac
if [ "${#release_id}" -gt 96 ]; then
    echo "Release ID exceeds 96 characters." >&2
    exit 2
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

recipe_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$recipe_dir/../../.." && pwd)
cd "$repo_dir"
revision=$(git rev-parse HEAD)
# Record when a validation build contains changes outside the recorded commit.
if [ -n "$(git status --porcelain --untracked-files=normal -- cmd internal platform go.mod go.sum)" ]; then
    revision="$revision-dirty"
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
    -var "release_id=$release_id" \
    -var "guest_agent_release=$guest_agent_release" \
    -var "source_revision=$revision" \
    -var "agent_binary=$build_dir/shiftbox-agent" \
    -var "agent_sha256=${agent_sha%% *}" \
    -var "guest_update_public_key=$build_dir/guest-update.pub" \
    -var "guest_update_key_sha256=${publisher_key_sha%% *}" \
    -var "manifest_path=$build_dir/manifest.json" \
    "$recipe_dir"

echo "AMI provenance manifest: $build_dir/manifest.json"
