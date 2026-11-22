#!/usr/bin/env bash
# Stages one candidate's regional AMIs, then shares the frozen set separately.
set -euo pipefail
umask 077
export AWS_PAGER=''

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

fail() {
  manifest_fail "$@" || exit 1
}
case "${1:-}:$#" in
  packer-handoff:5|prepare:6|share:6) ;;
  *) fail 'usage: amis.sh {packer-handoff|prepare|share} <rc-tag> <source-commit> <publisher-account> <packer-manifest> [<ami-stage>]' ;;
esac
operation=$1 tag=$2 commit=$3 publisher=$4 packer_manifest=$5
regions=(us-east-1 us-east-2 us-west-1 us-west-2)
canonical=099720109477
[[ -f "$packer_manifest" && ! -L "$packer_manifest" ]] || fail 'expected a regular Packer build manifest'
name="shiftbox-$tag-amd64"

raw=$(jq -ces --arg tag "$tag" --arg commit "$commit" --arg owner "$publisher" --arg canonical "$canonical" "$manifest_filter"'
  select(length == 1 and ($tag | candidate and length <= 96) and ($commit | commit) and
    ($owner | matches("^[0-9]{12}"))) | .[0] |
  .last_run_uuid as $uuid | .builds | select(length == 1) | .[0] |
  select(.builder_type == "amazon-ebs" and .packer_run_uuid == $uuid and
    ($uuid | matches("^[a-fA-F0-9]{8}(-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12}"))) |
  .custom_data as $data | $data.region as $region |
  select((supported_regions | index($region)) != null and
    ($data | shape(["provider", "region", "sourceRegion", "sourceAmiId", "sourceAmiOwnerId", "sourceRevision",
      "publicBuild", "publisherAccountId", "architecture", "platformVersion", "protocolVersion",
      "releaseId", "guestAgentRelease", "minimumRootDiskGiB", "guestAgentSHA256", "guestUpdateKeySHA256",
      "toolchainRecipeSHA256", "codeServerRecipeSHA256"]) and
      .provider == "ec2" and .sourceRegion == $region and .sourceAmiOwnerId == $canonical and
      (.sourceAmiId | ami) and .sourceRevision == $commit and .publisherAccountId == $owner and .publicBuild == "true" and
      .architecture == "amd64" and .platformVersion == "24.04" and .protocolVersion == "1" and
      .releaseId == $tag and .guestAgentRelease == $tag and .minimumRootDiskGiB == "32" and
      all(.guestAgentSHA256,.guestUpdateKeySHA256,.toolchainRecipeSHA256,.codeServerRecipeSHA256; sha256))) |
  select(.artifact_id | startswith($region + ":") and (ltrimstr($region + ":") | ami)) |
  {last_run_uuid:$uuid, builds:[{builder_type,artifact_id,packer_run_uuid,custom_data}]}
' "$packer_manifest") || fail 'Packer manifest lacks exact public-build identity'
if [[ "$operation" == packer-handoff ]]; then
  printf '%s\n' "$raw"
  exit 0
fi
stage=${6%/}
[[ -n "$stage" ]] || fail 'expected a stage directory'
source_region=$(jq -r '.builds[0].custom_data.region' <<<"$raw")
base_ami=$(jq -r '.builds[0].custom_data.sourceAmiId' <<<"$raw")
build_ami=$(jq -r '.builds[0].artifact_id | split(":")[1]' <<<"$raw")
observed=$(aws sts get-caller-identity --region "$source_region" --query Account --output text)
[[ "$observed" == "$publisher" ]] || fail 'AWS session is outside the approved publisher account'

[[ ! -L "$stage" && ( ! -e "$stage" || -d "$stage" ) ]] || fail 'invalid regional AMI stage'
mkdir -p -- "$(dirname -- "$stage")"
lock="$stage.lock"
mkdir -- "$lock" || fail 'another operation is using this regional AMI stage'
scratch=''
cleanup() {
  if [[ -n "$scratch" ]]; then rm -rf -- "$scratch"; fi
  rmdir -- "$lock"
}
trap cleanup EXIT
scratch=$(mktemp -d)
mkdir -- "$scratch/catalog"
mkdir -p -- "$stage"

keep_exact() {
  if [[ -L "$2" || -e "$2" ]]; then
    if [[ ! -f "$2" || -L "$2" ]] || ! cmp -s "$1" "$2"; then
      fail "frozen regional AMI data changed: ${2##*/}"
    fi
  else
    cp -- "$1" "$2"
  fi
}

jq -n --arg tag "$tag" --arg commit "$commit" --arg publisher "$publisher" \
  --arg manifest "$(manifest_sha256 "$packer_manifest")" --arg image "$build_ami" --arg region "$source_region" '
    {candidateTag:$tag,sourceCommit:$commit,publisherAccountId:$publisher,packerSha256:$manifest,buildAmi:$image,buildRegion:$region}
' > "$scratch/source.json"
frozen="$stage/workspace-images.json"
if [[ "$operation" == share || -e "$frozen" ]]; then
  [[ -f "$stage/.ami-source.json" && -f "$frozen" && ! -L "$frozen" ]] || fail 'regional AMIs must be prepared and frozen first'
fi
keep_exact "$scratch/source.json" "$stage/.ami-source.json"
for path in "$stage/catalog" "$frozen"; do
  [[ ! -L "$path" ]] || fail 'unexpected link in regional AMI stage'
done

find_named_copy() {
  aws ec2 describe-images --region "$1" --owners "$publisher" --filters "Name=name,Values=$name" --output json |
    jq -er "$manifest_filter"'
      .Images | if type != "array" or length > 1 then error("ambiguous regional image")
        elif length == 0 then "" else .[0].ImageId | select(ami) end'
}

describe_exact() {
  aws ec2 describe-images --region "$1" --owners "$publisher" --image-ids "$2" --output json |
    jq -ce --arg id "$2" --arg owner "$publisher" '
      .Images | select(length == 1) | .[0] | select(.ImageId == $id and .OwnerId == $owner)'
}

source_image=$(describe_exact "$source_region" "$build_ami") || fail 'cannot identify the exact built AMI'
cluster=$(jq -er "$manifest_filter"' [.Tags[] | select(.Key == "cluster_name") | .Value] |
  select(length == 1) | .[0] | select(matches("^[A-Za-z0-9._-]{1,128}"))' <<<"$source_image")
copy_tags=$(jq -c --arg source "$build_ami" --arg region "$source_region" '
  [.Tags[] | select(.Key | IN("project","cluster_name","environment","ShiftBoxArtifact","ShiftBoxRelease",
    "ShiftBoxSourceAMI","ShiftBoxSourceCommit"))] +
    [{Key:"ShiftBoxBuildAMI",Value:$source},{Key:"ShiftBoxBuildRegion",Value:$region}] as $tags |
  [{ResourceType:"image",Tags:$tags},{ResourceType:"snapshot",Tags:$tags}]
' <<<"$source_image")

validate_lineage() {
  jq -e --arg tag "$tag" --arg commit "$commit" --arg base "$base_ami" --arg build "$build_ami" \
    --arg source "$source_region" --arg region "$1" --arg name "$name" --arg cluster "$cluster" '
      (.Tags | map({key:.Key,value:.Value}) | from_entries) as $tags |
      $tags.project == "shiftbox" and $tags.cluster_name == $cluster and $tags.ShiftBoxArtifact == "workspace-ami" and
      $tags.ShiftBoxRelease == $tag and $tags.ShiftBoxSourceAMI == $base and $tags.ShiftBoxSourceCommit == $commit and
      (if $region == $source then .ImageId == $build else
       .Name == $name and .SourceImageId == $build and .SourceImageRegion == $source end) and
      (.State | IN("available", "pending"))'
}

image_permission() {
  aws ec2 describe-image-attribute --region "$1" --image-id "$2" --attribute launchPermission --output json |
    jq -er --arg image "$2" '
      select(.ImageId == $image) | (.LaunchPermissions // []) |
      if . == [] then "private" elif . == [{Group:"all"}] then "public" else error("unexpected launch permissions") end'
}

verify_region() {
  local region=$1 image_id=$2 image snapshot permission no_snapshot_grants
  image=$(describe_exact "$region" "$image_id") || fail "$region: cannot identify the exact regional AMI"
  validate_lineage "$region" <<<"$image" >/dev/null || fail "$region: inconsistent regional AMI lineage"
  if [[ "$(jq -r '.State' <<<"$image")" == pending ]]; then
    [[ "$operation" == prepare ]] || fail "$region: candidate image is not available"
    aws ec2 wait image-available --region "$region" --image-ids "$image_id"
    image=$(describe_exact "$region" "$image_id")
    validate_lineage "$region" <<<"$image" >/dev/null || fail "$region: inconsistent regional AMI lineage"
  fi
  snapshot=$(jq -er "$manifest_filter"'
    select(.State == "available" and .Architecture == "x86_64" and .RootDeviceName == "/dev/sda1" and
      .RootDeviceType == "ebs" and .VirtualizationType == "hvm" and (.Public | type == "boolean") and
      ((.ProductCodes // []) | length == 0) and
      (.DeprecationTime == null or (try (.DeprecationTime | sub("[.][0-9]+Z$";"Z") | fromdateiso8601) catch 0) > now)) |
    [.BlockDeviceMappings[] | select(.Ebs != null)] | select(length == 1) | .[0] |
    select(.DeviceName == "/dev/sda1" and .Ebs.Encrypted == false and .Ebs.VolumeSize == 32) |
    .Ebs.SnapshotId | select(matches("^snap-[a-f0-9]{17}"))' <<<"$image") ||
    fail "$region: AMI is unavailable, expired, or has an unexpected root"
  aws ec2 describe-snapshots --region "$region" --owner-ids "$publisher" --snapshot-ids "$snapshot" --output json |
    jq -e --arg owner "$publisher" --arg snapshot "$snapshot" '
      .Snapshots | length == 1 and all(.[]; .SnapshotId == $snapshot and .OwnerId == $owner and
        .Encrypted == false and .State == "completed" and .VolumeSize == 32)' >/dev/null ||
    fail "$region: root snapshot is not a clean publisher-owned snapshot"
  no_snapshot_grants=$(aws ec2 describe-snapshot-attribute --region "$region" --snapshot-id "$snapshot" \
    --attribute createVolumePermission --output json)
  jq -e --arg snapshot "$snapshot" '.SnapshotId == $snapshot and (.CreateVolumePermissions // []) == []' \
    <<<"$no_snapshot_grants" >/dev/null || fail "$region: root snapshot has unexpected sharing"
  permission=$(image_permission "$region" "$image_id") || fail "$region: AMI permissions are ambiguous"
  if [[ "$permission" == public ]]; then
    jq -e '.Public == true' <<<"$image" >/dev/null || fail "$region: AMI public status is inconsistent"
    [[ -f "$frozen" ]] || fail "$region: public AMI lacks frozen candidate data"
  else
    jq -e '.Public == false' <<<"$image" >/dev/null || fail "$region: AMI private status is inconsistent"
  fi
  printf '%s\n' "$permission" > "$scratch/permission-$region"
  jq --arg region "$region" --arg image "$image_id" '
    .builds[0].artifact_id = ($region + ":" + $image) |
    .builds[0].custom_data |= (.region = $region | del(.publicBuild, .publisherAccountId))
  ' <<<"$raw" > "$scratch/catalog/$region.json"
  jq -n --arg image "$image_id" --arg snapshot "$snapshot" --arg base "$base_ami" \
    --arg source "$source_region" --arg commit "$commit" --arg tag "$tag" --arg region "$region" \
    --arg catalogHash "$(manifest_sha256 "$scratch/catalog/$region.json")" '
    {amiId:$image,rootDeviceName:"/dev/sda1",rootSnapshotId:$snapshot,sourceAmiId:$base,sourceRegion:$source,
      sourceCommit:$commit,guestRelease:$tag,architecture:"amd64",minimumDiskGiB:32,
      catalogPath:("catalog/"+$region+".json"),catalogSha256:$catalogHash}
  ' > "$scratch/image-$region.json"
}

# Preflight every Region and all existing matches before copying or sharing.
for region in "${regions[@]}"; do
  id=$build_ami
  if [[ "$region" != "$source_region" ]]; then
    id=$(find_named_copy "$region") || fail "$region: multiple or invalid candidate images"
  fi
  if [[ -f "$frozen" ]]; then
    pinned=$(jq -er --arg region "$region" "$manifest_filter"' .[$region].amiId | select(ami)' "$frozen")
    [[ "$id" == "$pinned" ]] || fail "$region: frozen AMI is absent or no longer matches"
  fi
  if [[ -n "$id" ]]; then
    verify_region "$region" "$id"
  else
    [[ "$operation" == prepare ]] || fail "$region: frozen AMI is absent"
    encrypted=$(aws ec2 get-ebs-encryption-by-default --region "$region" --query EbsEncryptionByDefault --output text)
    case "$encrypted" in
      false|False) ;;
      *) fail "$region: EBS encryption by default prevents an unencrypted public copy" ;;
    esac
  fi
  printf '%s\n' "$id" > "$scratch/ami-$region"
done

if [[ "$operation" == prepare ]]; then
  for region in "${regions[@]}"; do
    id=$(cat "$scratch/ami-$region")
    if [[ -z "$id" ]]; then
      hash=$(printf '%s\n' "$tag" "$commit" "$publisher" "$source_region" "$build_ami" "$region" | openssl dgst -sha256 -r)
      token=${hash%% *}
      if copied=$(aws ec2 copy-image --region "$region" --name "$name" --source-region "$source_region" \
        --source-image-id "$build_ami" --no-encrypted --no-copy-image-tags --client-token "$token" \
        --tag-specifications "$copy_tags" --output json); then
        id=$(jq -er "$manifest_filter"' .ImageId | select(ami)' <<<"$copied")
      else
        id=$(find_named_copy "$region") || fail "$region: copy response and regional identity are ambiguous"
        [[ -n "$id" ]] || fail "$region: copy response is uncertain. Retry the same candidate to reconcile."
      fi
      verify_region "$region" "$id"
    fi
  done
fi

jq -n --slurpfile east1 "$scratch/image-us-east-1.json" --slurpfile east2 "$scratch/image-us-east-2.json" \
  --slurpfile west1 "$scratch/image-us-west-1.json" --slurpfile west2 "$scratch/image-us-west-2.json" '
  {"us-east-1":$east1[0],"us-east-2":$east2[0],"us-west-1":$west1[0],"us-west-2":$west2[0]}
' > "$scratch/workspace-images.json"
mkdir -p -- "$stage/catalog"
for region in "${regions[@]}"; do
  keep_exact "$scratch/catalog/$region.json" "$stage/catalog/$region.json"
done
keep_exact "$scratch/workspace-images.json" "$frozen"

if [[ "$operation" == share ]]; then
  for region in "${regions[@]}"; do
    if [[ "$(cat "$scratch/permission-$region")" == private ]]; then
      block=$(aws ec2 get-image-block-public-access-state --region "$region" \
        --query ImageBlockPublicAccessState --output text)
      [[ "$block" == unblocked ]] || fail "$region: publisher AMI public access is blocked"
      id=$(cat "$scratch/ami-$region")
      # EC2 reports successful DryRun authorization as an error. Check every
      # remaining private AMI before allowing the first real share request.
      if dry_run=$(aws ec2 modify-image-attribute --region "$region" --image-id "$id" \
        --attribute launchPermission --operation-type add --user-groups all --dry-run 2>&1) ||
        [[ "$dry_run" != *'(DryRunOperation)'* ]]; then
        fail "$region: the scoped public launch permission did not pass EC2 DryRun"
      fi
    fi
  done
  for region in "${regions[@]}"; do
    id=$(cat "$scratch/ami-$region")
    if [[ "$(cat "$scratch/permission-$region")" == private ]]; then
      # Re-read after a lost response as well as a reported success. Never undo partial sharing.
      aws ec2 modify-image-attribute --region "$region" --image-id "$id" \
        --attribute launchPermission --operation-type add --user-groups all >/dev/null || true
    fi
    for attempt in 1 2 3 4 5; do
      if [[ "$(image_permission "$region" "$id")" == public ]] &&
        describe_exact "$region" "$id" | jq -e '.Public == true' >/dev/null; then
        break
      fi
      if [[ "$attempt" == 5 ]]; then fail "$region: public permission is unverified. Retry the same frozen candidate."; fi
      sleep 2
    done
  done
fi
cat -- "$frozen"
