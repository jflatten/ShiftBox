#!/bin/sh
set -eu
umask 077

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ] || [ -z "$1" ]; then
    echo "Usage: build-image.sh <guest-release> <guest-update-public-key> [amd64|arm64]" >&2
    exit 2
fi
case "$1" in
    *[!a-zA-Z0-9._-]*) echo "Guest release contains unsupported characters." >&2; exit 2 ;;
esac

architecture=${3:-amd64}
case "$architecture" in
    amd64) ;;
    arm64)
        if [ "$(uname -m)" != arm64 ]; then
            echo "A native ARM64 UTM build requires an Apple Silicon Mac." >&2
            exit 2
        fi ;;
    *) echo "Architecture must be amd64 or arm64." >&2; exit 2 ;;
esac

case "$2" in
    /*) publisher_key=$2 ;;
    *) publisher_key=$PWD/$2 ;;
esac
if [ ! -f "$publisher_key" ] || [ "$(wc -c < "$publisher_key")" -ne 32 ]; then
    echo "The Ed25519 guest update public key must contain 32 raw bytes." >&2
    exit 2
fi

recipe_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH='' cd -- "$recipe_dir/../.." && pwd)
cd "$root"
. "$recipe_dir/code-server.env"
if [ "$architecture" = arm64 ]; then
    CODE_SERVER_SHA256=$CODE_SERVER_ARM64_SHA256
fi
revision=$(git rev-parse HEAD)
# Mark a local validation build if its source differs from the recorded commit.
if [ -n "$(git status --porcelain --untracked-files=normal -- cmd internal platform go.mod go.sum)" ]; then
    revision="$revision-dirty"
fi
mkdir -p "$root/.cache/packer"
build_dir=$(mktemp -d "$root/.cache/packer/build.XXXXXXXX")
trap 'rm -f "$build_dir/ssh-key" "$build_dir/ssh-key.pub"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cp "$publisher_key" "$build_dir/guest-update.pub"
ssh-keygen -q -t ed25519 -N '' -C '' -f "$build_dir/ssh-key"
CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" go build -trimpath -ldflags "-X main.agentRelease=$1"     -o "$build_dir/shiftbox-agent" ./cmd/shiftbox-agent

echo "Build directory: $build_dir"
packer build \
    -var "architecture=$architecture" \
    -var "build_name=shiftbox-${build_dir##*/}" \
    -var "output_directory=$build_dir/export" \
    -var "build_public_key=$(cat "$build_dir/ssh-key.pub")" \
    -var "build_private_key=$build_dir/ssh-key" \
    -var "agent_binary=$build_dir/shiftbox-agent" \
    -var "guest_update_public_key=$build_dir/guest-update.pub" \
    "$recipe_dir/shiftbox.pkr.hcl"

mkdir "$build_dir/final"
/usr/bin/osascript "$recipe_dir/prepare-export.applescript" \
    "$build_dir/export/shiftbox.utm" "$build_dir/final/shiftbox.utm"
platform_release=$1
if [ "$architecture" = arm64 ]; then
    platform_release="$1-arm64"
fi
agent_sha=$(shasum -a 256 "$build_dir/shiftbox-agent" | cut -d ' ' -f 1)
cat > "$build_dir/build-metadata.json" <<EOF
{
  "schemaVersion": 1,
  "releaseId": "$platform_release",
  "architecture": "$architecture",
  "platformVersion": "24.04",
  "protocolVersion": "1",
  "guest": {"version": "$1", "sha256": "$agent_sha"},
  "ide": {"version": "$CODE_SERVER_VERSION", "sha256": "$CODE_SERVER_SHA256", "source": "$CODE_SERVER_SOURCE"},
  "provenance": {"source": "https://github.com/jflatten/ShiftBox", "revision": "$revision"}
}
EOF
go run ./platform/packer/manifest "$build_dir/final/shiftbox.utm" < "$build_dir/build-metadata.json" > "$build_dir/manifest.json"
mv "$build_dir/manifest.json" "$build_dir/final/manifest.json"
echo "Prepared artifact: $build_dir/final"
