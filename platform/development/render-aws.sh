#!/usr/bin/env bash
# Render the current checkout using a verified public baseline and optional private pins.
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
mkdir "$scratch/catalog"
for region in us-east-1 us-east-2 us-west-1 us-west-2; do
  tar -xOzf "$baseline/shiftbox-aws-support.tar.gz" "catalog/$region.json" >"$scratch/catalog/$region.json"
done
jq '{schemaVersion,candidateTag,sourceCommit,runtimeCompatibility,publisherAccountId,controller,postgresImage,
     regions:(.regions|map_values({hostAmiId,workspaceImage}))}' "$manifest" >"$scratch/pins.json"
bash "$root/platform/release/bundle.sh" "$scratch/pins.json" "$scratch/catalog" "$root/docs/site" "$scratch/rendered"
python3 "$here/aws_template.py" "$scratch/rendered/shiftbox-aws-cloudformation.yaml" "$manifest" \
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
