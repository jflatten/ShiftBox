#!/bin/sh
# Stage the license and notice files for the modules in the controller binary.
set -eu

if [ "$#" -ne 2 ]; then
    echo "Usage: $0 CONTROLLER_BINARY DESTINATION" >&2
    exit 2
fi
binary=$1
destination=$2
build_info="$(go version -m "$binary")"
modules="$(printf '%s\n' "$build_info" | awk '$1 == "dep" { print $2, $3 }')"
if [ -z "$modules" ]; then
    echo "Controller dependency information is missing" >&2
    exit 1
fi
mkdir -p "$destination/modules"
cp LICENSE "$destination/ShiftBox-LICENSE"
printf '%s\n' "$modules" > "$destination/modules.txt"

printf '%s\n' "$modules" | while read -r module version; do
    directory="$(go list -m -f '{{.Dir}}' "$module@$version")"
    if [ -z "$directory" ] || [ ! -d "$directory" ]; then
        echo "Source is unavailable for $module $version" >&2
        exit 1
    fi
    target="$destination/modules/$module@$version"
    mkdir -p "$target"
    found=false
    for file in "$directory"/LICENSE* "$directory"/LICENCE* "$directory"/COPYING* \
        "$directory"/NOTICE* "$directory"/COPYRIGHT* "$directory"/PATENTS*; do
        if [ -f "$file" ]; then
            install -m 0644 "$file" "$target/"
            found=true
        fi
    done
    if [ "$found" != true ]; then
        echo "License or notice is unavailable for $module $version" >&2
        exit 1
    fi
done
