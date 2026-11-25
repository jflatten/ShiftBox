#!/usr/bin/env bash
# Publish only the public guides and branding mark.
set -euo pipefail

site=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$site/../.." && pwd)

if [[ $# -ne 1 || -z "${1%/}" || -e "$1" || -L "$1" ]]; then
    echo 'Usage: docs/site/build.sh <new-output-directory>' >&2
    exit 2
fi
output=${1%/}
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
source_dir="$scratch/source"
mkdir -p -- "$source_dir"/{_layouts,assets}

copy_public() {
    if [[ ! -f "$1" || -L "$1" ]]; then
        printf 'Missing regular public site input: %s\n' "${1##*/}" >&2
        exit 1
    fi
    cp -- "$1" "$2"
}

for guide in index install setup-helper configure private-access first-workspace troubleshooting upgrade-recovery contributing; do
    copy_public "$site/$guide.md" "$source_dir/$guide.md"
done
copy_public "$site/_layouts/default.html" "$source_dir/_layouts/default.html"
copy_public "$root/branding/png/shiftbox-logo-light-1600.png" "$source_dir/assets/shiftbox-logo.png"
for screenshot in setup-administrator.png setup-activate.png; do
    copy_public "$site/assets/$screenshot" "$source_dir/assets/$screenshot"
done

export BUNDLE_GEMFILE=${BUNDLE_GEMFILE:-"$site/Gemfile"}
bundle exec jekyll build --source "$source_dir" --config "$site/_config.yml" --destination "$scratch/site"
mkdir -p -- "$(dirname -- "$output")"
mv -- "$scratch/site" "$output"
