#!/usr/bin/env bash
# Prepares a local RC payload and finalizes it after independently verified versioned URLs exist.
set -euo pipefail
umask 022

usage() {
  cat >&2 <<'HELP'
Usage:
  build-candidate.sh prepare <pins.json> <catalog-directory> <public-guide-directory> <stage-directory>
  build-candidate.sh finalize <pins.json> <template-locations.json> <stage-directory>
HELP
  exit 2
}

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

case "${1:-}:$#" in
  prepare:5) operation=$1 pins=$2 catalog=$3 guides=$4 stage=$5 ;;
  finalize:4) operation=$1 pins=$2 locations=$3 stage=$4 ;;
  *) usage ;;
esac
[[ -n "$stage" && "${stage%/}" != "" ]] || usage
stage=${stage%/}
mkdir -p -- "$(dirname -- "$stage")"
lock="$stage.lock"
mkdir -- "$lock" || { manifest_fail 'another process is using this candidate stage'; exit 1; }
scratch=''
cleanup() {
  if [[ -n "$scratch" ]]; then
    rm -rf -- "$scratch"
  fi
  rmdir -- "$lock"
}
trap cleanup EXIT
scratch=$(mktemp -d "$(dirname -- "$stage")/.shiftbox-candidate.XXXXXXXX")

# A prepared stage is reusable only when every existing artifact matches.
check_destination() {
  local artifact=$1 target=$2
  if [[ -L "$target" || -e "$target" ]] && { [[ ! -f "$target" || -L "$target" ]] || ! cmp -s "$artifact" "$target"; }; then
    manifest_fail "candidate already has different bytes for ${target##*/}"
    return 1
  fi
}

asset_checksums() {
  local directory=$1
  jq -n --arg template "$(manifest_sha256 "$directory/shiftbox-aws-cloudformation.yaml")" \
    --arg installer "$(manifest_sha256 "$directory/install.sh")" \
    --arg support "$(manifest_sha256 "$directory/shiftbox-aws-support.tar.gz")" '
    {template:{name:"shiftbox-aws-cloudformation.yaml",sha256:$template},
     installer:{name:"install.sh",sha256:$installer},
     supportAssets:{name:"shiftbox-aws-support.tar.gz",sha256:$support}}
  '
}

if [[ "$operation" == prepare ]]; then
  "$here/bundle.sh" "$pins" "$catalog" "$guides" "$scratch/assets"
  asset_checksums "$scratch/assets" >"$scratch/checksums"
  jq --arg pins "$(manifest_sha256 "$pins")" '. + {pinsSha256: $pins}' \
    "$scratch/checksums" >"$scratch/assets/.candidate-stage.json"

  if [[ -L "$stage" || ( -e "$stage" && ! -d "$stage" ) ]]; then
    manifest_fail 'candidate stage must be an ordinary directory'
    exit 1
  fi
  mkdir -p -- "$stage"
  artifacts=(shiftbox-aws-cloudformation.yaml install.sh shiftbox-aws-support.tar.gz .candidate-stage.json)
  for name in "${artifacts[@]}"; do
    check_destination "$scratch/assets/$name" "$stage/$name"
  done
  for name in "${artifacts[@]}"; do
    if [[ ! -e "$stage/$name" ]]; then
      mv -- "$scratch/assets/$name" "$stage/$name"
    fi
  done
  exit 0
fi

for path in "$stage" "$stage/.candidate-stage.json" "$stage/shiftbox-aws-cloudformation.yaml" "$stage/install.sh" "$stage/shiftbox-aws-support.tar.gz"; do
  [[ ! -L "$path" && -e "$path" ]] || { manifest_fail 'candidate stage is incomplete'; exit 1; }
done
asset_checksums "$stage" >"$scratch/checksums"
jq -e --arg pin_hash "$(manifest_sha256 "$pins")" --slurpfile expected "$scratch/checksums" '
  .pinsSha256 == $pin_hash and del(.pinsSha256) == $expected[0]
' "$stage/.candidate-stage.json" >/dev/null || {
  manifest_fail 'candidate pins or staged artifact bytes changed'
  exit 1
}

# Object locations are supplied by the checked publisher workflow, never invented by packaging.
jq -e --slurpfile locations "$locations" --slurpfile checksums "$scratch/checksums" "$manifest_filter"'
  if ($locations | length == 1 and (.[0] | shape(supported_regions) and
    all(.[]; shape(["bucket", "key", "versionId"])) and ([.[].bucket] | unique | length) == 4)) then
    . + $checksums[0] | . as $release |
    .regions |= with_entries(.key as $region | .value.templateLocation =
      ($locations[0][$region] + {sha256: $release.template.sha256} |
        .url = location_url($region) | .quickCreateUrl = console_url($region; .url)))
  else error("expected one minimal object location for each release region") end
' <"$pins" >"$scratch/distribution-manifest.json"

tag=$(jq -er '.candidateTag' <"$pins")
source_commit=$(jq -er '.sourceCommit' <"$pins")
publisher=$(jq -er '.publisherAccountId' <"$pins")
manifest_validate "$scratch/distribution-manifest.json" "$tag" "$source_commit" "$publisher"
manifest_verify_assets "$scratch/distribution-manifest.json" "$stage"
check_destination "$scratch/distribution-manifest.json" "$stage/distribution-manifest.json"
if [[ ! -e "$stage/distribution-manifest.json" ]]; then
  mv -- "$scratch/distribution-manifest.json" "$stage/distribution-manifest.json"
fi
