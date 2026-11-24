#!/bin/sh
set -eu
umask 022

if [ "$#" -ne 2 ] || [ "$(uname -s)" != Darwin ]; then
    echo 'Usage on macOS: package.sh <management HTTPS origin | --portable> <output directory>' >&2
    exit 2
fi
origin=$1
case "$origin" in
    *[!A-Za-z0-9.:/-]*) echo 'Provide a ShiftBox HTTPS origin without a path or credentials.' >&2; exit 2 ;;
esac
if [ "$origin" != --portable ] && ! printf '%s\n' "$origin" | grep -Eq '^https://[A-Za-z0-9.-]+(:[1-9][0-9]{0,4})?$'; then
    echo 'Provide a ShiftBox HTTPS origin without a path or credentials.' >&2
    exit 2
fi

mkdir -p "$2"
output=$(CDPATH='' cd -- "$2" && pwd)
bundle_name='ShiftBox Local Helper.app'
image_name='ShiftBox-Local-Helper.dmg'
if [ -e "$output/$bundle_name" ] || [ -e "$output/$image_name" ]; then
    echo 'Choose an output directory without an existing ShiftBox helper build.' >&2
    exit 2
fi
packaging=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH='' cd -- "$packaging/../../.." && pwd)
stage=$(mktemp -d "$output/.helper.XXXXXXXX")
mount="$stage/mounted"
mounted=false
cleanup() {
    if [ "$mounted" = true ]; then
        hdiutil detach "$mount" -quiet || return
    fi
    rm -rf "$stage"
}
trap cleanup EXIT
volume="$stage/volume"
app="$volume/$bundle_name"
contents="$app/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources"
cp "$packaging/Info.plist" "$contents/Info.plist"
config="$contents/Resources/shiftbox-helper.json"
plutil -create xml1 "$stage/config.plist"
if [ "$origin" = --portable ]; then
    plutil -insert portable -bool true "$stage/config.plist"
else
    plutil -insert origin -string "$origin" "$stage/config.plist"
fi
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

# Set the Finder open folder without requiring a running Finder during CI.
ln -s /Applications "$volume/Applications"
hdiutil makehybrid -quiet -hfs -hfs-openfolder "$volume" -hfs-volume-name 'ShiftBox Local Helper' \
    -o "$stage/volume.dmg" "$volume"

# Hybrid authoring adds FinderInfo attributes that strict app verification rejects.
hdiutil convert -quiet "$stage/volume.dmg" -format UDRW -o "$stage/editable.dmg"
mkdir "$mount"
hdiutil attach -readwrite -nobrowse -mountpoint "$mount" -quiet "$stage/editable.dmg"
mounted=true
xattr -dr com.apple.FinderInfo "$mount/$bundle_name"
codesign --verify --deep --strict "$mount/$bundle_name"
hdiutil detach "$mount" -quiet
mounted=false
hdiutil convert -quiet "$stage/editable.dmg" -format UDZO -o "$stage/$image_name"

mv "$app" "$output/$bundle_name"
mv "$stage/$image_name" "$output/$image_name"
shasum -a 256 "$output/$image_name"
