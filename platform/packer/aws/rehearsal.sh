#!/usr/bin/env bash
# Guard an operator-approved hosted rehearsal and remove its private artifacts.
set -euo pipefail
umask 077
candidate=v1.0.0-rc.1
region=us-east-2

fail() { printf 'Packer rehearsal: %s\n' "$1" >&2; exit 1; }

attempt_context() {
  [[ "$REHEARSAL_ATTEMPT" =~ ^rehearsal-[a-z0-9]{1,8}$ &&
     "$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID" =~ ^[0-9]{12}$ ]] ||
    fail 'use a reviewed publisher and rehearsal attempt'
  jq -e --arg attempt "$REHEARSAL_ATTEMPT" --arg account "$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID" \
    '.attempt_id==$attempt and .account==$account and .region=="us-east-2"' \
    <<< "$SHIFTBOX_RELEASE_BUILD_NETWORK" >/dev/null || fail 'the Ohio network handoff does not match'
}

validate() {
  [[ "$GITHUB_EVENT_NAME" == workflow_dispatch && "$GITHUB_REPOSITORY" == jflatten/ShiftBox &&
     "$GITHUB_REPOSITORY_ID" == 1457179716 && "$GITHUB_REPOSITORY_OWNER_ID" == 11561330 &&
     "$GITHUB_REF" == refs/heads/main && "$GITHUB_ACTOR_ID" == 11561330 &&
     "$GITHUB_TRIGGERING_ACTOR" == jflatten && "$GITHUB_RUN_ATTEMPT" == 1 &&
     "$SHIFTBOX_PACKER_REHEARSAL_ENABLED" == true ]] || fail 'the rehearsal dispatch is disabled or out of scope'
  [[ "$REHEARSAL_SOURCE" =~ ^[0-9a-f]{40}$ && "$REHEARSAL_SOURCE" == "$GITHUB_SHA" ]] ||
    fail 'dispatch main at the exact reviewed commit'
  attempt_context
  [[ "$REHEARSAL_ROLE_ARN" == "arn:aws:iam::$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:role/shiftbox-publisher-packer-rehearsal" ]] ||
    fail 'use the dedicated rehearsal build role'
  count=$(gh run list --repo jflatten/ShiftBox --workflow ci.yml --branch main --commit "$GITHUB_SHA" \
    --status success --limit 1 --json databaseId --jq length)
  [[ "$count" == 1 ]] || fail 'main needs passing Checks on the reviewed commit'
}

inventory() {
  local account="$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID"
  local filters=(
    Name=tag:project,Values=shiftbox Name=tag:cluster_name,Values=shiftbox-publisher
    "Name=tag:environment,Values=release-$candidate" "Name=tag:publisher_attempt,Values=$REHEARSAL_ATTEMPT"
  )
  aws --region "$region" ec2 describe-images --owners self --filters "${filters[@]}" > "$scratch/images.json"
  aws --region "$region" ec2 describe-snapshots --owner-ids self --filters "${filters[@]}" > "$scratch/snapshots.json"
  jq -e --arg account "$account" --arg attempt "$REHEARSAL_ATTEMPT" --arg candidate "$candidate" '
    def tags: (.Tags // [] | map({key:.Key,value:.Value}) | from_entries);
    .Images | select(type=="array") | length<=1 and all(.[]; tags as $tags |
      (.ImageId|test("^ami-[0-9a-f]{17}$")) and .OwnerId==$account and .Public==false and
      .Name==("shiftbox-"+$attempt+"-amd64") and
      $tags.project=="shiftbox" and $tags.cluster_name=="shiftbox-publisher" and
      $tags.environment==("release-"+$candidate) and $tags.publisher_attempt==$attempt and
      ($tags.ShiftBoxArtifact==null or
        ($tags.ShiftBoxArtifact=="packer-rehearsal" and $tags.ShiftBoxRelease==$attempt)))
  ' "$scratch/images.json" >/dev/null || fail 'found an image outside the verified private rehearsal'
  jq -e --arg account "$account" --arg attempt "$REHEARSAL_ATTEMPT" --arg candidate "$candidate" '
    .Snapshots | select(type=="array") | length<=1 and
    all(.[]; (.Tags // [] | map({key:.Key,value:.Value}) | from_entries) as $tags |
      (.SnapshotId|test("^snap-[0-9a-f]{17}$")) and .OwnerId==$account and .VolumeSize==32 and
      $tags.project=="shiftbox" and $tags.cluster_name=="shiftbox-publisher" and
      $tags.environment==("release-"+$candidate) and $tags.publisher_attempt==$attempt and
      ($tags.ShiftBoxArtifact==null or
        ($tags.ShiftBoxArtifact=="packer-rehearsal" and $tags.ShiftBoxRelease==$attempt)))
  ' "$scratch/snapshots.json" >/dev/null || fail 'found a snapshot outside the verified rehearsal'
}

case "${1:-}" in
  validate) validate ;;
  empty|cleanup)
    attempt_context
    [[ "$(aws --region "$region" sts get-caller-identity --query Account --output text)" == "$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID" ]] ||
      fail 'AWS caller does not match the publisher'
    scratch=$(mktemp -d)
    trap 'rm -rf -- "$scratch"' EXIT
    inventory
    image=$(jq -r '.Images[0].ImageId // empty' "$scratch/images.json")
    snapshot=$(jq -r '.Snapshots[0].SnapshotId // empty' "$scratch/snapshots.json")
    if [[ "$1" == empty ]]; then
      [[ -z "$image" && -z "$snapshot" ]] || fail 'attempt already has artifacts, use staffed teardown'
    else
      if [[ -n "$snapshot" ]]; then
        aws --region "$region" ec2 describe-snapshot-attribute --snapshot-id "$snapshot" --attribute createVolumePermission |
          jq -e '.CreateVolumePermissions == []' >/dev/null || fail 'the snapshot has unexpected sharing permissions'
      fi
      if [[ -n "$image" ]]; then
        jq -e --arg snapshot "$snapshot" '
          [.Images[0].BlockDeviceMappings[].Ebs.SnapshotId] == [$snapshot] and $snapshot!=""
        ' "$scratch/images.json" >/dev/null || fail 'the image root snapshot is outside the verified inventory'
        aws --region "$region" ec2 describe-image-attribute --image-id "$image" --attribute launchPermission |
          jq -e '.LaunchPermissions == []' >/dev/null || fail 'the image has unexpected sharing permissions'
        aws --region "$region" ec2 deregister-image --image-id "$image" >/dev/null
      fi
      if [[ -n "$snapshot" ]]; then
        for delay in 0 5 10 15; do
          sleep "$delay"
          if aws --region "$region" ec2 delete-snapshot --snapshot-id "$snapshot" >/dev/null 2>"$scratch/delete.err"; then
            break
          fi
          grep -q 'InvalidSnapshot.InUse' "$scratch/delete.err" || fail 'snapshot cleanup needs staffed review'
        done
      fi
      # Bound the eventual-consistency wait so the operator can reconcile before the lease expires.
      for delay in 0 5 10 15; do
        sleep "$delay"
        inventory
        if jq -e '.Images==[]' "$scratch/images.json" >/dev/null &&
           jq -e '.Snapshots==[]' "$scratch/snapshots.json" >/dev/null; then
          exit 0
        fi
      done
      fail 'artifact inventory has not converged, complete staffed teardown'
    fi
    ;;
  *) fail 'expected validate, empty, or cleanup' ;;
esac
