#!/usr/bin/env bash
# Prepare inputs for the single published template or an older downloaded baseline.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
if [[ $# -lt 2 ]]; then
  printf 'Usage: %s <downloaded baseline assets directory> <new output directory> [--controller IMAGE@DIGEST] [--workspace PACKER_MANIFEST] [--one-pass | --launch-form FORM]\n' "$0" >&2
  exit 2
fi
baseline=$1
output=$2
shift 2
manifest="$baseline/distribution-manifest.json"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

bash "$root/platform/release/manifest.sh" verify-assets "$manifest" "$baseline"
if [[ $(jq -er '.schemaVersion' "$manifest") == 2 ]]; then
  if (($#)); then
    printf '%s\n' 'Use the release controller and workspace pins for a fresh one-template install.' >&2
    exit 2
  fi
  bash "$root/platform/release/manifest.sh" launch-pins "$manifest" us-east-2 >"$scratch/release-pins.json"
  mkdir -- "$output"
  cp -- "$scratch/release-pins.json" "$output/"
  printf 'Use the verified release YAML with Release inputs from %s/release-pins.json in Ohio.\n' "$output"
  exit 0
fi
mkdir "$scratch/catalog"
tar -xOzf "$baseline/shiftbox-aws-support.tar.gz" catalog/us-east-2.json >"$scratch/catalog/us-east-2.json"
python3 "$here/aws_template.py" "$baseline/shiftbox-aws-cloudformation.yaml" "$manifest" \
  "$scratch/shiftbox-aws-development.json" "$scratch/catalog/us-east-2.json" "$@"
mkdir -- "$output"
cp -- "$scratch/shiftbox-aws-development.json" "$output/"
policy="$root/platform/cloudformation/aws/stack-policy.json"
cp -- "$policy" "$output/"
if jq -e '.Resources.SetupTask' "$output/shiftbox-aws-development.json" >/dev/null; then
  cp -- "$root/platform/cloudformation/aws/setup.sh" "$output/"
fi
cp -- "$scratch/catalog/us-east-2.json" "$output/published-workspace-catalog.json"
printf 'Upload %s/shiftbox-aws-development.json in the Ohio testing account.\n' "$output"
