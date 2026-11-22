#!/usr/bin/env bash
# Publish only the public guides, branding mark and verified release links.
set -euo pipefail

site=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$site/../.." && pwd)
# shellcheck source=platform/release/manifest.sh
source "$root/platform/release/manifest.sh"

if [[ $# -ne 1 || -z "${1%/}" || -e "$1" || -L "$1" ]]; then
    echo 'Usage: docs/site/build.sh <new-output-directory>' >&2
    exit 2
fi
output=${1%/}
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
source_dir="$scratch/source"
mkdir -p -- "$source_dir"/{_layouts,_data,assets}
release_url='https://github.com/jflatten/ShiftBox/releases'
release_api='https://api.github.com/repos/jflatten/ShiftBox/releases'
publisher=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}

copy_public() {
    if [[ ! -f "$1" || -L "$1" ]]; then
        printf 'Missing regular public site input: %s\n' "${1##*/}" >&2
        exit 1
    fi
    cp -- "$1" "$2"
}

for guide in index install private-access troubleshooting upgrade-recovery contributing; do
    copy_public "$site/$guide.md" "$source_dir/$guide.md"
done
copy_public "$site/_layouts/default.html" "$source_dir/_layouts/default.html"
copy_public "$root/branding/png/shiftbox-logo-light-1600.png" "$source_dir/assets/shiftbox-logo.png"

fetch() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 5 --max-time 20 "$1"
}

eligible_release() {
    jq -ec --arg canonical "$release_url" "$manifest_filter"'
      select(type == "object" and .immutable == true and .draft == false and
        (.prerelease | type == "boolean") and (.published_at | type == "string" and length > 0)) |
      select(if .prerelease then (.tag_name | candidate) else (.tag_name | matches("^" + semver)) end) |
      select(.html_url == ($canonical + "/tag/" + .tag_name))
    ' <<<"$1"
}

verified_regions() {
    local release=$1 tag=$2 asset expected url actual commit
    asset=$(jq -ce --arg url "$release_url/download/$tag/distribution-manifest.json" "$manifest_filter"'
      [.assets[]? | select(.name == "distribution-manifest.json")] |
      select(length == 1) | .[0] | select(.state == "uploaded" and .browser_download_url == $url and
        (.digest | matches("^sha256:[0-9a-f]{64}"))) | {url:$url,digest}
    ' <<<"$release") || return 1
    url=$(jq -r .url <<<"$asset")
    expected=$(jq -r '.digest | ltrimstr("sha256:")' <<<"$asset")
    commit=$(awk -v ref="refs/tags/$tag" '
      $2 == ref { direct = $1 }
      $2 == ref "^{}" { peeled = $1 }
      END { print peeled != "" ? peeled : direct }
    ' "$scratch/refs")
    [[ "$commit" =~ ^[a-f0-9]{40}$ ]] || return 1
    # Use the published immutable manifest bytes. The source tag and expected
    # publisher come from the remote Git ref and the maintainer's configuration.
    fetch "$url" >"$scratch/manifest.json" 2>/dev/null || return 1
    actual=$(manifest_sha256 "$scratch/manifest.json") || return 1
    [[ "$actual" == "$expected" ]] || return 1
    manifest_validate "$scratch/manifest.json" "$tag" "$commit" "$publisher" 2>/dev/null || return 1
    jq -c '[.regions | to_entries[] | {region:.key,url:.value.templateLocation.quickCreateUrl}]' "$scratch/manifest.json"
}

release_view() {
    local release=$1 tag links
    tag=$(jq -r .tag_name <<<"$release")
    links=$(verified_regions "$release" "$tag") || links='[]'
    jq -c --argjson links "$links" '{tag:.tag_name,url:.html_url,prerelease,regions:$links}' <<<"$release"
}

stable=null
stable_tag=''
: >"$scratch/versions.jsonl"
if [[ "$publisher" =~ ^[0-9]{12}$ ]]; then
    # The latest stable is resolved independently of the explicit release list.
    git ls-remote --tags https://github.com/jflatten/ShiftBox.git >"$scratch/refs" 2>/dev/null || : >"$scratch/refs"
    if latest=$(fetch "$release_api/latest" 2>/dev/null) &&
        latest=$(eligible_release "$latest") &&
        jq -e '.prerelease == false' <<<"$latest" >/dev/null; then
        stable=$(release_view "$latest")
        stable_tag=$(jq -r .tag <<<"$stable")
    fi

    # Retain version-specific links even when the latest stable endpoint has no release.
    page=1
    while releases=$(fetch "$release_api?per_page=100&page=$page" 2>/dev/null) &&
        jq -e 'type == "array"' <<<"$releases" >/dev/null; do
        while IFS= read -r entry; do
            if release=$(eligible_release "$entry"); then
                tag=$(jq -r .tag_name <<<"$release")
                if [[ "$tag" != "$stable_tag" ]]; then
                    release_view "$release" >>"$scratch/versions.jsonl"
                fi
            fi
        done < <(jq -c '.[]' <<<"$releases")
        if [[ "$(jq 'length' <<<"$releases")" -lt 100 ]]; then break; fi
        page=$((page + 1))
    done
fi
jq -n --argjson stable "$stable" --slurpfile versions "$scratch/versions.jsonl" \
    '{stable:$stable,versions:($versions | unique_by(.tag) | reverse)}' >"$source_dir/_data/releases.json"

export BUNDLE_GEMFILE=${BUNDLE_GEMFILE:-"$site/Gemfile"}
bundle exec jekyll build --source "$source_dir" --config "$site/_config.yml" --destination "$scratch/site"
mkdir -p -- "$(dirname -- "$output")"
mv -- "$scratch/site" "$output"
