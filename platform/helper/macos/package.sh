#!/bin/sh
set -eu
umask 022

if [ "$#" -ne 2 ] || [ "$(uname -s)" != Darwin ]; then
    echo 'Usage on the MVP Mac: package.sh <management HTTPS origin> <output directory>' >&2
    exit 2
fi
origin=$1
case "$origin" in
    *[!A-Za-z0-9.:/-]*) echo 'Provide a ShiftBox HTTPS origin without a path or credentials.' >&2; exit 2 ;;
esac
if ! printf '%s\n' "$origin" | grep -Eq '^https://[A-Za-z0-9.-]+(:[1-9][0-9]{0,4})?$'; then
    echo 'Provide a ShiftBox HTTPS origin without a path or credentials.' >&2
    exit 2
fi

mkdir -p "$2"
output=$(CDPATH='' cd -- "$2" && pwd)
bundle_name='ShiftBox Local Helper.app'
archive_name='ShiftBox-Local-Helper.zip'
if [ -e "$output/$bundle_name" ] || [ -e "$output/$archive_name" ]; then
    echo 'Choose an output directory without an existing ShiftBox helper build.' >&2
    exit 2
fi
packaging=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH='' cd -- "$packaging/../../.." && pwd)
stage=$(mktemp -d "$output/.helper.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
app="$stage/$bundle_name"
contents="$app/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources"
cp "$packaging/Info.plist" "$contents/Info.plist"
config="$contents/Resources/shiftbox-helper.json"
plutil -create xml1 "$stage/config.plist"
plutil -insert origin -string "$origin" "$stage/config.plist"
plutil -convert json -o "$config" "$stage/config.plist"

cd "$repo"
CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0 "${GO:-go}" build -trimpath \
    -o "$contents/MacOS/shiftbox-helper" ./cmd/shiftbox-helper

# Use ShiftBox's existing mark at native macOS icon sizes.
source_icon="$repo/branding/repository/shiftbox-avatar-dark-1024.png"
iconset="$stage/ShiftBox.iconset"
mkdir "$iconset"
for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$source_icon" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    doubled=$((size * 2))
    sips -z "$doubled" "$doubled" "$source_icon" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns -o "$contents/Resources/ShiftBox.icns" "$iconset"

codesign --force --sign - "$app"
codesign --verify --deep --strict "$app"
ditto -c -k --keepParent "$app" "$stage/$archive_name"
mv "$app" "$output/$bundle_name"
mv "$stage/$archive_name" "$output/$archive_name"
shasum -a 256 "$output/$archive_name"
