#!/bin/sh
set -eu
umask 077

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
    echo "Usage: build-image.sh <guest-release>" >&2
    exit 2
fi
case "$1" in
    *[!a-zA-Z0-9._-]*) echo "Guest release contains unsupported characters." >&2; exit 2 ;;
esac

recipe_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH='' cd -- "$recipe_dir/../.." && pwd)
cd "$root"
mkdir -p "$root/.cache/packer"
build_dir=$(mktemp -d "$root/.cache/packer/build.XXXXXXXX")
trap 'rm -f "$build_dir/ssh-key" "$build_dir/ssh-key.pub"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

ssh-keygen -q -t ed25519 -N '' -C '' -f "$build_dir/ssh-key"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X main.agentRelease=$1"     -o "$build_dir/shiftbox-agent" ./cmd/shiftbox-agent

echo "Build directory: $build_dir"
packer build     -var "build_name=shiftbox-${build_dir##*/}"     -var "output_directory=$build_dir/export"     -var "build_public_key=$(cat "$build_dir/ssh-key.pub")"     -var "build_private_key=$build_dir/ssh-key"     -var "agent_binary=$build_dir/shiftbox-agent"     "$recipe_dir/shiftbox.pkr.hcl"

mkdir "$build_dir/final"
/usr/bin/osascript "$recipe_dir/prepare-export.applescript" \
    "$build_dir/export/shiftbox.utm" "$build_dir/final/shiftbox.utm"
echo "Prepared bundle: $build_dir/final/shiftbox.utm"
