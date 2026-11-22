#!/bin/sh
# Verify the staged archive contains the signed, branded app and its exact controller origin.
set -eu
umask 077
packaging=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
origin=https://shiftbox.example.test:8443

if sh "$packaging/package.sh" http://shiftbox.example.test "$tmp"; then
    echo 'Packaging accepted an insecure origin.' >&2
    exit 1
fi

sh "$packaging/package.sh" "$origin" "$tmp"
archive="$tmp/ShiftBox-Local-Helper.zip"
mkdir "$tmp/unpacked"
ditto -x -k "$archive" "$tmp/unpacked"
app="$tmp/unpacked/ShiftBox Local Helper.app"
plist="$app/Contents/Info.plist"
test "$(plutil -extract CFBundleIdentifier raw "$plist")" = com.shiftbox.local-helper
test "$(plutil -extract CFBundleExecutable raw "$plist")" = shiftbox-helper
test "$(plutil -extract LSUIElement raw "$plist")" = true
test "$(plutil -extract origin raw "$app/Contents/Resources/shiftbox-helper.json")" = "$origin"
test -s "$app/Contents/Resources/ShiftBox.icns"
test -x "$app/Contents/MacOS/shiftbox-helper"
codesign --verify --deep --strict "$app"
codesign -dvv "$app" 2>&1 | grep -Fq 'Signature=adhoc'
