#!/usr/bin/env bash
set -euo pipefail
umask 077
export AWS_CLI_AUTO_PROMPT=off AWS_PAGER=""

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

promotion_verify() {
  local manifest=$1 qualification=$2 assets=$3 release=$4 stable=$5 commit=$6 publisher=$7 manifest_hash
  if [[ "$stable" == *-rc.* ]] || ! manifest_validate "$manifest" "$stable" "$commit" "$publisher"; then
    manifest_fail "promotion requires the matching stable version and peeled RC source"
    return 1
  fi
  if ! cmp -s "$manifest" "$assets/distribution-manifest.json" || ! manifest_verify_assets "$manifest" "$assets"; then
    manifest_fail "candidate assets do not match the supplied manifest"
    return 1
  fi
  manifest_hash=$(manifest_sha256 "$manifest") || return 1
  if ! jq -es --slurpfile payload "$manifest" --slurpfile rc "$release" --arg hash "$manifest_hash" "$manifest_filter"'
    length == 1 and ($payload | length) == 1 and ($rc | length) == 1 and
    (.[0] as $q | $payload[0] as $p | $rc[0] as $r |
    ([$p.installer, $p.template, $p.supportAssets, {name:"distribution-manifest.json",sha256:$hash}]) as $assets |
    ([$assets[].name] | sort) as $names |
    $q.schemaVersion == 1 and $q.cleanupVerified == true and
    ($q | shape(["schemaVersion", "candidate", "regionalLaunches", "freshInstall", "gateResults", "cleanupVerified"])) and
    ($r | .tag_name == $p.candidateTag and .target_commitish == $p.sourceCommit and
      .prerelease == true and .draft == false and .immutable == true and
      (.id | type == "number" and . > 0 and floor == .)) and
    ($q.candidate |
      shape(["tag","sourceCommit","releaseId","assets","controllerDigest","templateSha256"]) and
      .tag == $p.candidateTag and .sourceCommit == $p.sourceCommit and .releaseId == $r.id and
      .controllerDigest == $p.controller.digest and .templateSha256 == $p.template.sha256) and
    ($q.candidate.assets | shape($names)) and ([$r.assets[].name] | sort) == $names and
    all($assets[]; . as $actual |
      $q.candidate.assets[.name] as $evidence |
      ($evidence | shape(["id","sha256"]) and .sha256 == $actual.sha256 and
        (.id | type == "number" and . > 0 and floor == .)) and
      any($r.assets[]; .name == $actual.name and .id == $evidence.id and
        .state == "uploaded" and .digest == ("sha256:" + $actual.sha256))) and
    ($q.regionalLaunches | shape(supported_regions)) and
    all(supported_regions[]; . as $region |
      $p.regions[$region] as $ref |
      ($q.regionalLaunches[$region] |
        shape(["amiId","publisherAccountId","versionId","templateSha256","publicBoot","encryptedRoot",
          "hostedTemplateValidated","cleanupVerified"]) and
        .amiId == $ref.workspaceImage.amiId and .publisherAccountId == $p.publisherAccountId and
        .versionId == $ref.templateLocation.versionId and .templateSha256 == $ref.templateLocation.sha256 and
        .publicBoot == true and .encryptedRoot == true and .hostedTemplateValidated == true and .cleanupVerified == true)) and
    ($q.freshInstall == {region:"us-east-2",consoleIde:true,privateHttps:true,protectedSetup:true,anonymousPull:true,cliResume:true}) and
    ($q.gateResults == ([range(1;15) | {key:("V"+tostring),value:"pass"}] | from_entries)))
  ' <"$qualification" >/dev/null; then
    manifest_fail "reviewed qualification does not prove the exact public candidate and all release gates"
    return 1
  fi
}

promotion_verify_live() {
  local manifest=$1 inspected=$2 publisher actual now region image root snapshot
  "$here/templates.sh" verify "$manifest" "$inspected" || return 1
  manifest_verify_controller "$(jq -er '.controller.repository' <"$manifest")" \
    "$(jq -er '.controller.digest' <"$manifest")" || return 1
  publisher=$(jq -er '.publisherAccountId' <"$manifest") || return 1
  actual=$(aws --region us-east-2 sts get-caller-identity --query Account --output text) || return 1
  if [ "$actual" != "$publisher" ]; then
    manifest_fail "live AMI checks require the approved publisher account"
    return 1
  fi
  now=$(date -u '+%Y-%m-%dT%H:%M:%SZ') || return 1
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    image=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.amiId' <"$manifest") || return 1
    root=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.rootDeviceName' <"$manifest") || return 1
    snapshot=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.rootSnapshotId' <"$manifest") || return 1
    if ! aws --region "$region" ec2 describe-images --owners "$publisher" --image-ids "$image" --include-deprecated --output json |
        jq -e --arg owner "$publisher" --arg image "$image" --arg root "$root" --arg snapshot "$snapshot" --arg now "$now" '
          (.Images | length) == 1 and (.Images[0] |
            .OwnerId == $owner and .ImageId == $image and .State == "available" and
            .Public == true and .Architecture == "x86_64" and .VirtualizationType == "hvm" and
            .RootDeviceType == "ebs" and .RootDeviceName == $root and
            (.DeprecationTime == null or (.DeprecationTime | sub("[.][0-9]+Z$"; "Z")) > $now) and
            ([.BlockDeviceMappings[] | select(.Ebs != null)] | length) == 1 and
            any(.BlockDeviceMappings[]; .DeviceName == $root and .Ebs.SnapshotId == $snapshot and .Ebs.Encrypted != true))' >/dev/null ||
        ! aws --region "$region" ec2 describe-image-attribute --attribute launchPermission --image-id "$image" --output json |
          jq -e 'any(.LaunchPermissions[]; .Group == "all")' >/dev/null ||
        ! aws --region "$region" ec2 describe-snapshots --owner-ids "$publisher" --snapshot-ids "$snapshot" --output json |
          jq -e --arg owner "$publisher" --arg snapshot "$snapshot" '
            (.Snapshots | length) == 1 and (.Snapshots[0] |
              .OwnerId == $owner and .SnapshotId == $snapshot and .State == "completed" and .Encrypted == false)' >/dev/null; then
      manifest_fail "the qualified workspace AMI is no longer an exact, public, available image in $region"
      return 1
    fi
  done
}

case "${1:-}:$#" in
  verify:8) promotion_verify "$2" "$3" "$4" "$5" "$6" "$7" "$8" ;;
  verify-live:3) promotion_verify_live "$2" "$3" ;;
  *) manifest_fail "usage: promote.sh verify manifest qualification assets rc-release stable-tag source publisher | verify-live manifest template" ;;
esac
