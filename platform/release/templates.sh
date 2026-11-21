#!/usr/bin/env bash
set -euo pipefail
umask 077
export AWS_PAGER="" AWS_CLI_AUTO_PROMPT=off

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

template_read_version() {
  local url=$1 destination=$2
  curl --fail --silent --show-error --proto '=https' --connect-timeout 10 --max-time 90 \
    --output "$destination" "$url"
}

template_verify() (
  local manifest=$1 inspected=$2 scratch region url
  jq -es "$manifest_filter"' length == 1 and (.[0] | distribution)' <"$manifest" >/dev/null || return 1
  [ "$(manifest_sha256 "$inspected")" = "$(jq -er '.template.sha256' <"$manifest")" ] || return 1
  scratch=$(mktemp -d)
  trap 'rm -rf -- "$scratch"' EXIT
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    url=$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' <"$manifest") || return 1
    if ! template_read_version "$url" "$scratch/read.yaml" || ! cmp -s "$inspected" "$scratch/read.yaml"; then
      manifest_fail "hosted template version differs or cannot be read in $region"
      return 1
    fi
  done
)

template_stage() (
  local account=$1 tag=$2 inspected=$3 bucket_map=$4 actual hash key bucket region location
  local scratch ver listed result='{}'
  if ! [[ "$account" =~ ^[0-9]{12}$ ]] ||
      ! jq -en --arg tag "$tag" "$manifest_filter"' $tag | candidate' >/dev/null; then
    manifest_fail "expected an approved publisher account and a canonical RC tag"
    return 1
  fi
  if ! jq -es "$manifest_filter"'
    length == 1 and (.[0] | shape(supported_regions) and all(.[]; bucket) and
      (([.[]] | unique | length) == 4))
  ' <"$bucket_map" >/dev/null; then
    manifest_fail "expected four dedicated, distinct publisher buckets"
    return 1
  fi
  [ -f "$inspected" ] && [ -s "$inspected" ] && [ "$(wc -c <"$inspected")" -le 1048576 ] || return 1
  actual=$(aws --region us-east-2 sts get-caller-identity --query Account --output text) || return 1
  if [ "$actual" != "$account" ]; then
    manifest_fail "AWS credentials do not match the approved publisher account"
    return 1
  fi

  # Verify every destination before staging the first object version.
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    bucket=$(jq -er --arg region "$region" '.[$region]' <"$bucket_map") || return 1
    location=$(aws --region "$region" s3api get-bucket-location --bucket "$bucket" \
      --expected-bucket-owner "$account" --output json | jq -er '.LocationConstraint // "us-east-1"') || return 1
    if [ "$location" != "$region" ] ||
        ! aws --region "$region" s3api get-bucket-versioning --bucket "$bucket" \
          --expected-bucket-owner "$account" --output json | jq -e '.Status == "Enabled"' >/dev/null; then
      manifest_fail "the $region publisher bucket has unexpected region or versioning settings"
      return 1
    fi
  done

  hash=$(manifest_sha256 "$inspected") || return 1
  key="public/templates/$tag/$hash/shiftbox-aws-cloudformation.yaml"
  scratch=$(mktemp -d)
  trap 'rm -rf -- "$scratch"' EXIT
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    bucket=$(jq -er --arg region "$region" '.[$region]' <"$bucket_map") || return 1
    listed=$(aws --region "$region" s3api list-object-versions --bucket "$bucket" --prefix "$key" \
      --expected-bucket-owner "$account" --output json) || return 1
    ver=$(jq -er --arg key "$key" '
      if any(.DeleteMarkers[]?; .Key == $key) then error("unexpected delete marker") else
        [.Versions[]? | select(.Key == $key)] |
        if length == 0 then "" elif length == 1 and .[0].IsLatest then .[0].VersionId
        else error("conflicting template versions") end
      end
    ' <<<"$listed") || return 1
    if [ -z "$ver" ]; then
      ver=$(aws --region "$region" s3api put-object --bucket "$bucket" --key "$key" --body "$inspected" \
        --expected-bucket-owner "$account" --content-type application/yaml --if-none-match '*' \
        --server-side-encryption AES256 --checksum-algorithm SHA256 \
        --tagging "project=shiftbox&cluster_name=$tag" --output json | jq -er '.VersionId') || return 1
    fi
    if ! jq -en --arg ver "$ver" "$manifest_filter"' $ver | object_version' >/dev/null; then
      manifest_fail "the $region template does not have a valid immutable object version"
      return 1
    fi
    location=$(jq -ner --arg region "$region" --arg bucket "$bucket" --arg key "$key" --arg version "$ver" \
      "$manifest_filter"' {bucket:$bucket,key:$key,versionId:$version} | location_url($region)') || return 1
    manifest_quick_create "$region" "$location" >/dev/null || return 1
    if ! template_read_version "$location" "$scratch/read.yaml" || ! cmp -s "$inspected" "$scratch/read.yaml"; then
      manifest_fail "hosted template version differs or cannot be read in $region"
      return 1
    fi
    result=$(jq -cn --argjson previous "$result" --arg region "$region" --arg bucket "$bucket" \
      --arg key "$key" --arg version "$ver" '$previous + {($region):{bucket:$bucket,key:$key,versionId:$version}}') || return 1
  done
  printf '%s\n' "$result"
)
case "${1:-}:$#" in
  stage:5) template_stage "$2" "$3" "$4" "$5" ;;
  verify:3) template_verify "$2" "$3" ;;
  *) manifest_fail "usage: templates.sh stage account candidate template buckets.json | verify manifest template" ;;
esac
