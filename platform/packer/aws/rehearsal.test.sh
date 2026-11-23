#!/usr/bin/env bash
set -euo pipefail
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
export GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_REPOSITORY_ID=1457179716 GITHUB_REPOSITORY_OWNER_ID=11561330
export GITHUB_ACTOR_ID=11561330 GITHUB_TRIGGERING_ACTOR=jflatten GITHUB_EVENT_NAME=workflow_dispatch
export GITHUB_REF=refs/heads/main GITHUB_RUN_ATTEMPT=1 GITHUB_RUN_ID=101
export GITHUB_SHA=0123456789abcdef0123456789abcdef01234567
export SHIFTBOX_PACKER_REHEARSAL_ENABLED=true REHEARSAL_ATTEMPT=rehearsal-fixture REHEARSAL_SOURCE="$GITHUB_SHA"
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export REHEARSAL_ROLE_ARN=arn:aws:iam::111122223333:role/shiftbox-publisher-packer-rehearsal
export SHIFTBOX_RELEASE_BUILD_NETWORK='{"account":"111122223333","region":"us-east-2","attempt_id":"rehearsal-fixture"}'
export PATH="$fixture:$PATH"
cat > "$fixture/gh" <<'STUB'
#!/usr/bin/env bash
test "$1 $2" = "run list"
printf '%s\n' "${CHECKS_COUNT:-1}"
STUB
chmod +x "$fixture/gh"

validate() { bash "$here/rehearsal.sh" validate; }

# Permit only a first dispatch by the sole maintainer on the reviewed main commit.
validate
for input in 'GITHUB_EVENT_NAME=push' 'GITHUB_REF=refs/heads/rehearsal' 'GITHUB_REPOSITORY_ID=1' \
    'GITHUB_ACTOR_ID=1' 'GITHUB_TRIGGERING_ACTOR=someone' 'GITHUB_RUN_ATTEMPT=2' \
    'SHIFTBOX_PACKER_REHEARSAL_ENABLED=false' 'REHEARSAL_ATTEMPT=release-rc' \
    'REHEARSAL_SOURCE=not-the-dispatched-commit' 'REHEARSAL_ROLE_ARN=arn:aws:iam::111122223333:role/release' \
    'CHECKS_COUNT=0'; do
    if env "$input" bash "$here/rehearsal.sh" validate >/dev/null 2>&1; then
        printf 'Rehearsal accepted an unapproved dispatch condition: %s\n' "$input" >&2
        exit 1
    fi
done

# A result created with the release marker must never pass the rehearsal cleanup filter.
export AWS_DEFAULT_REGION=us-east-2
cat > "$fixture/aws" <<'STUB'
#!/usr/bin/env bash
set -eu
test "$1 $2" = "--region us-east-2"
shift 2
case "$1 $2" in
  'sts get-caller-identity') printf '%s\n' 111122223333 ;;
  'ec2 describe-images')
    if grep -qx 'ec2 deregister-image' "$MUTATION_TRACE"; then echo '{"Images":[]}'; else echo "$IMAGES"; fi ;;
  'ec2 describe-snapshots')
    if grep -qx 'ec2 delete-snapshot' "$MUTATION_TRACE"; then echo '{"Snapshots":[]}'; else echo "$SNAPSHOTS"; fi ;;
  'ec2 describe-image-attribute') echo '{"LaunchPermissions":[]}' ;;
  'ec2 describe-snapshot-attribute') echo '{"CreateVolumePermissions":[]}' ;;
  'ec2 deregister-image'|'ec2 delete-snapshot') printf '%s\n' "$1 $2" >> "$MUTATION_TRACE" ;;
  *) exit 2 ;;
esac
STUB
chmod +x "$fixture/aws"
export MUTATION_TRACE="$fixture/mutated"
export SNAPSHOTS='{"Snapshots":[]}'
touch "$MUTATION_TRACE"
if IMAGES='{"Images":[{"ImageId":"ami-00000000000000001","OwnerId":"111122223333","Public":false,"Name":"shiftbox-v1.0.0-rc.1-amd64","Tags":[{"Key":"ShiftBoxArtifact","Value":"workspace-ami"}]}]}' \
    bash "$here/rehearsal.sh" cleanup >/dev/null 2>&1; then
    printf 'Rehearsal cleanup accepted an unrelated image.\n' >&2
    exit 1
fi
test ! -s "$MUTATION_TRACE"

# The Canonical source has two instance-store mappings. Only its root mapping
# represents a snapshot. Cleanup can delete the AMI and that exact snapshot.
tags='[{"Key":"project","Value":"shiftbox"},{"Key":"cluster_name","Value":"shiftbox-publisher"},{"Key":"environment","Value":"release-v1.0.0-rc.1"},{"Key":"publisher_attempt","Value":"rehearsal-fixture"},{"Key":"ShiftBoxArtifact","Value":"packer-rehearsal"},{"Key":"ShiftBoxRelease","Value":"rehearsal-fixture"}]'
export IMAGES SNAPSHOTS
IMAGES=$(jq -cn --argjson tags "$tags" '{Images:[{ImageId:"ami-00000000000000001",Name:"shiftbox-rehearsal-fixture-amd64",OwnerId:"111122223333",Public:false,Tags:$tags,
  RootDeviceName:"/dev/sda1",BlockDeviceMappings:[
    {DeviceName:"/dev/sda1",Ebs:{SnapshotId:"snap-00000000000000002"}},
    {DeviceName:"/dev/sdb",VirtualName:"ephemeral0"},
    {DeviceName:"/dev/sdc",VirtualName:"ephemeral1"}
  ]}]}')
SNAPSHOTS=$(jq -cn --argjson tags "$tags" '{Snapshots:[{SnapshotId:"snap-00000000000000002",OwnerId:"111122223333",VolumeSize:32,Tags:$tags}]}')
if bash "$here/rehearsal.sh" empty >/dev/null 2>&1; then exit 1; fi
test ! -s "$MUTATION_TRACE"

# A mismatched root or a second EBS mapping still blocks all deletions.
for edit in '.Images[0].BlockDeviceMappings[0].DeviceName="/dev/sdz"' \
    '.Images[0].BlockDeviceMappings[0].Ebs.SnapshotId="snap-00000000000000003"' \
    '.Images[0].BlockDeviceMappings += [{DeviceName:"/dev/sdd",Ebs:{SnapshotId:"snap-00000000000000003"}}]'; do
    if IMAGES=$(jq -c "$edit" <<< "$IMAGES") bash "$here/rehearsal.sh" cleanup >/dev/null 2>&1; then
        printf 'Rehearsal cleanup accepted an unexpected EBS mapping: %s\n' "$edit" >&2
        exit 1
    fi
    test ! -s "$MUTATION_TRACE"
done

bash "$here/rehearsal.sh" cleanup
diff -u <(printf '%s\n' 'ec2 deregister-image' 'ec2 delete-snapshot') "$MUTATION_TRACE"
printf 'Packer rehearsal authority and cleanup isolation fixtures passed.\n'
