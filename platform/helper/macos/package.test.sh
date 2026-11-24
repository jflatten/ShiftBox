#!/bin/sh
# Verify the mounted installer supports drag-to-Applications and carries the signed app.
set -eu
umask 077
packaging=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
mount="$tmp/mounted"
mounted=false
cleanup() {
    if [ "$mounted" = true ]; then
        hdiutil detach "$mount" -quiet || return
    fi
    rm -rf "$tmp"
}
trap cleanup EXIT
mkdir "$mount"
origin=https://shiftbox.example.test:8443

if sh "$packaging/package.sh" http://shiftbox.example.test "$tmp"; then
    echo 'Packaging accepted an insecure origin.' >&2
    exit 1
fi

sh "$packaging/package.sh" "$origin" "$tmp"
mount_installer() {
    image="$1/ShiftBox-Local-Helper.dmg"
    test ! -e "$1/ShiftBox-Local-Helper.zip"
    hdiutil verify "$image" -quiet
    hdiutil attach "$image" -readonly -nobrowse -mountpoint "$mount" -quiet
    mounted=true
    test "$(readlink "$mount/Applications")" = /Applications
    bless --info "$mount" | grep -Eq '^finderinfo\[2\]: +2 =>'
    codesign --verify --deep --strict "$mount/ShiftBox Local Helper.app"
}

mount_installer "$tmp"
app="$mount/ShiftBox Local Helper.app"
plist="$app/Contents/Info.plist"
test "$(plutil -extract CFBundleIdentifier raw "$plist")" = com.shiftbox.local-helper
test "$(plutil -extract CFBundleExecutable raw "$plist")" = shiftbox-helper
test "$(plutil -extract LSUIElement raw "$plist")" = true
test "$(plutil -extract origin raw "$app/Contents/Resources/shiftbox-helper.json")" = "$origin"
test -s "$app/Contents/Resources/ShiftBox.icns"
test -x "$app/Contents/MacOS/shiftbox-helper"
codesign -dvv "$app" 2>&1 | grep -Fq 'Signature=adhoc'
hdiutil detach "$mount" -quiet
mounted=false

# Portable controller images carry a signed app that asks for the installation URL.
sh "$packaging/package.sh" --portable "$tmp/portable"
mount_installer "$tmp/portable"
portable="$mount/ShiftBox Local Helper.app"
test "$(plutil -extract portable raw "$portable/Contents/Resources/shiftbox-helper.json")" = true
