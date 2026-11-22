#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
mkdir -p "$test_dir/bin" "$test_dir/cloud"
export PATH="$test_dir/bin:$PATH"
export AMI_TEST_STATE="$test_dir/cloud" AMI_TEST_TRACE="$test_dir/trace"
export AMI_TEST_TAG=v1.2.3-rc.4 AMI_TEST_COMMIT=1111111111111111111111111111111111111111
export AMI_TEST_PUBLISHER=111111111111 AMI_TEST_BASE=ami-0123456789abcdef0
export AMI_TEST_BUILD=ami-0123456789abcdef2 AMI_TEST_CASE=normal
manifest="$test_dir/packer.json"
stage="$test_dir/stage"

jq -n '
  "00000000-0000-4000-8000-000000000001" as $uuid |
  ([range(64)|"a"]|join("")) as $hash |
  {last_run_uuid:$uuid,builds:[{name:"fixture",builder_type:"amazon-ebs",packer_run_uuid:$uuid,
    artifact_id:("us-east-2:"+env.AMI_TEST_BUILD),custom_data:{provider:"ec2",region:"us-east-2",
      releaseId:env.AMI_TEST_TAG,guestAgentRelease:env.AMI_TEST_TAG,sourceRevision:env.AMI_TEST_COMMIT,
      sourceAmiId:env.AMI_TEST_BASE,sourceAmiOwnerId:"099720109477",sourceRegion:"us-east-2",
      publisherAccountId:env.AMI_TEST_PUBLISHER,publicBuild:"true",architecture:"amd64",
      minimumRootDiskGiB:"32",platformVersion:"24.04",protocolVersion:"1",guestAgentSHA256:$hash,
      guestUpdateKeySHA256:$hash,toolchainRecipeSHA256:$hash,codeServerRecipeSHA256:$hash}}]}
' > "$manifest"

cat > "$test_dir/bin/aws" <<'AWS'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$AMI_TEST_TRACE"
service=$1 action=$2
shift 2
region='' id='' snapshot='' attribute='' operation='' group='' dry_run=false token='' owner='' source_id='' source_region='' name=''
while [[ $# -gt 0 ]]; do
    case "$1" in
        --region) region=$2; shift ;;
        --image-id|--image-ids) id=$2; shift ;;
        --snapshot-id|--snapshot-ids) snapshot=$2; shift ;;
        --owners|--owner-ids) owner=$2; shift ;;
        --attribute) attribute=$2; shift ;;
        --operation-type) operation=$2; shift ;;
        --user-groups) group=$2; shift ;;
        --dry-run) dry_run=true ;;
        --client-token) token=$2; shift ;;
        --source-image-id) source_id=$2; shift ;;
        --source-region) source_region=$2; shift ;;
        --name) name=$2; shift ;;
    esac
    shift
done
case "$region" in
    us-east-1) digit=1 ;;
    us-east-2) digit=2 ;;
    us-west-1) digit=3 ;;
    us-west-2) digit=4 ;;
    *) exit 2 ;;
esac
expected="ami-0123456789abcdef$digit"
root_snapshot="snap-0123456789abcdef$digit"
public=false
[[ ! -f "$AMI_TEST_STATE/public-$region" ]] || public=true
case "$service $action" in
    'sts get-caller-identity')
        if [[ "$AMI_TEST_CASE" == wrong-account ]]; then echo 222222222222; else echo "$AMI_TEST_PUBLISHER"; fi
        ;;
    'ec2 get-ebs-encryption-by-default')
        if [[ "$AMI_TEST_CASE:$region" == encrypted-default:us-west-2 ]]; then echo True; else echo False; fi
        ;;
    'ec2 get-image-block-public-access-state')
        if [[ "$AMI_TEST_CASE:$region" == blocked-share:us-west-2 ]]; then echo block-new-sharing; else echo unblocked; fi
        ;;
    'ec2 describe-images')
        [[ "$owner" == "$AMI_TEST_PUBLISHER" ]] || exit 4
        if [[ "$AMI_TEST_CASE:$region" == hidden-copy:us-west-1 && -z "$id" ]]; then
            printf '{"Images":[]}\n'
            exit 0
        fi
        if [[ "$region" != us-east-2 && ! -f "$AMI_TEST_STATE/copied-$region" ]]; then
            printf '{"Images":[]}\n'
            exit 0
        fi
        [[ -z "$id" || "$id" == "$expected" ]] || exit 4
        copies=1
        observed_source=$AMI_TEST_BUILD
        if [[ "$AMI_TEST_CASE:$region" == duplicate:us-west-2 ]]; then copies=2; fi
        if [[ "$AMI_TEST_CASE:$region" == bad-lineage:us-west-2 ]]; then observed_source=ami-0123456789abcdef9; fi
        jq -cn --arg image "$expected" --arg snapshot "$root_snapshot" --arg region "$region" \
          --arg source "$observed_source" --argjson public "$public" --argjson copies "$copies" '
          {ImageId:$image,Name:("shiftbox-"+env.AMI_TEST_TAG+"-amd64"),OwnerId:env.AMI_TEST_PUBLISHER,Public:$public,
            Architecture:"x86_64",RootDeviceName:"/dev/sda1",RootDeviceType:"ebs",State:"available",
            VirtualizationType:"hvm",ProductCodes:[],
            BlockDeviceMappings:[{DeviceName:"/dev/sda1",Ebs:{SnapshotId:$snapshot,Encrypted:false,VolumeSize:32}}],
            Tags:[{Key:"project",Value:"shiftbox"},{Key:"cluster_name",Value:"release-builder"},
              {Key:"ShiftBoxArtifact",Value:"workspace-ami"},{Key:"ShiftBoxRelease",Value:env.AMI_TEST_TAG},
              {Key:"ShiftBoxSourceAMI",Value:env.AMI_TEST_BASE},{Key:"ShiftBoxSourceCommit",Value:env.AMI_TEST_COMMIT}]} |
            if $region == "us-east-2" then . else . + {SourceImageId:$source,SourceImageRegion:"us-east-2"} end |
            . as $image | {Images:[range($copies) | $image]}'
        ;;
    'ec2 describe-snapshots')
        [[ "$snapshot" == "$root_snapshot" && "$owner" == "$AMI_TEST_PUBLISHER" ]] || exit 4
        encrypted=false
        if [[ "$AMI_TEST_CASE:$region" == encrypted-copy:us-west-2 ]]; then encrypted=true; fi
        jq -cn --arg snapshot "$snapshot" --argjson encrypted "$encrypted" '
          {Snapshots:[{SnapshotId:$snapshot,OwnerId:env.AMI_TEST_PUBLISHER,State:"completed",VolumeSize:32,Encrypted:$encrypted}]}'
        ;;
    'ec2 describe-snapshot-attribute')
        [[ "$snapshot" == "$root_snapshot" ]] || exit 4
        jq -cn --arg snapshot "$snapshot" '{SnapshotId:$snapshot,CreateVolumePermissions:[]}'
        ;;
    'ec2 describe-image-attribute')
        [[ "$id" == "$expected" ]] || exit 4
        permission='[]'
        if [[ "$public" == true ]]; then permission='[{"Group":"all"}]'; fi
        if [[ "$AMI_TEST_CASE:$region" == private-grant:us-west-2 ]]; then permission='[{"UserId":"555555555555"}]'; fi
        jq -cn --arg image "$id" --argjson permission "$permission" '{ImageId:$image,LaunchPermissions:$permission}'
        ;;
    'ec2 copy-image')
        [[ "$region" != us-east-2 && "$source_region" == us-east-2 && "$source_id" == "$AMI_TEST_BUILD" &&
          "$name" == "shiftbox-$AMI_TEST_TAG-amd64" && "$token" =~ ^[a-f0-9]{64}$ ]] || exit 4
        touch "$AMI_TEST_STATE/copied-$region"
        case "$AMI_TEST_CASE:$region" in lost-copy:us-west-1|hidden-copy:us-west-1) exit 255 ;; esac
        jq -cn --arg image "$expected" '{ImageId:$image}'
        ;;
    'ec2 wait')
        ;;
    'ec2 modify-image-attribute')
        [[ "$id" == "$expected" && "$attribute:$operation:$group" == launchPermission:add:all ]] || exit 4
        if [[ "$dry_run" == true ]]; then
            if [[ "$AMI_TEST_CASE:$region" == denied-share:us-west-2 ]]; then
                echo 'An error occurred (UnauthorizedOperation)' >&2
            else
                echo 'An error occurred (DryRunOperation)' >&2
            fi
            exit 254
        fi
        touch "$AMI_TEST_STATE/public-$region"
        if [[ "$AMI_TEST_CASE:$region" == lost-share:us-west-1 ]]; then exit 255; fi
        ;;
    *) printf 'Unexpected mock call %s %s.\n' "$service" "$action" >&2; exit 2 ;;
esac
AWS
printf '#!/bin/sh\nexit 0\n' > "$test_dir/bin/sleep"
chmod +x "$test_dir/bin/"*

run() {
    : > "$AMI_TEST_TRACE"
    "$here/amis.sh" "$1" "$AMI_TEST_TAG" "$AMI_TEST_COMMIT" "$AMI_TEST_PUBLISHER" "$manifest" "${2:-$stage}"
}

reject() {
    if run "$@" > "$test_dir/rejected" 2>&1; then
        printf 'Regional AMI operation unexpectedly accepted %s.\n' "$AMI_TEST_CASE" >&2
        exit 1
    fi
}

reject_writes() {
    if grep -E '^ec2 (copy-image|modify-image-attribute|modify-snapshot-attribute|deregister-image|delete-snapshot) ' "$AMI_TEST_TRACE" |
        grep -qv ' --dry-run$'; then
        printf 'Unexpected AWS write during %s.\n' "$AMI_TEST_CASE" >&2
        exit 1
    fi
}

# The build handoff is locally validated and excludes non-allowlisted Packer data.
: > "$AMI_TEST_TRACE"
jq '.builds[0].files = ["/local/path/only"]' "$manifest" > "$test_dir/private-manifest.json"
"$here/amis.sh" packer-handoff "$AMI_TEST_TAG" "$AMI_TEST_COMMIT" "$AMI_TEST_PUBLISHER" \
    "$test_dir/private-manifest.json" > "$test_dir/build-handoff.json"
jq -e --arg ami "$AMI_TEST_BUILD" '.builds[0] | .artifact_id == ("us-east-2:"+$ami) and (has("files") | not)' \
    "$test_dir/build-handoff.json" >/dev/null
test ! -s "$AMI_TEST_TRACE"

# Publisher and account encryption checks prevent any regional copy on preflight failure.
for case_name in wrong-account encrypted-default; do
    export AMI_TEST_CASE=$case_name
    reject prepare "$test_dir/$case_name"
    reject_writes
done

# A lost copy response can resolve to one exact, tagged image without another copy.
export AMI_TEST_CASE=lost-copy
run prepare > /dev/null
test "$(grep -c '^ec2 copy-image ' "$AMI_TEST_TRACE")" -eq 3
test "$(grep -c '^ec2 copy-image .*--region us-west-1 ' "$AMI_TEST_TRACE")" -eq 1
if grep -Eq '^ec2 modify.*attribute ' "$AMI_TEST_TRACE"; then
    echo 'Prepare unexpectedly shared an AMI or snapshot.' >&2
    exit 1
fi
jq -e 'keys == ["us-east-1","us-east-2","us-west-1","us-west-2"] and
    .["us-west-1"].amiId == "ami-0123456789abcdef3" and
    all(.[]; .sourceRegion == "us-east-2" and .rootDeviceName == "/dev/sda1")' "$stage/workspace-images.json" >/dev/null
jq -e '.builds[0] | .artifact_id == "us-west-1:ami-0123456789abcdef3" and
    .custom_data.region == "us-west-1" and .custom_data.sourceRegion == "us-east-2" and
    (.custom_data | has("publisherAccountId") | not)' "$stage/catalog/us-west-1.json" >/dev/null
cp "$stage/workspace-images.json" "$test_dir/workspace-images.json"

# A rerun adopts the exact prior bytes and rechecks every pinned image.
export AMI_TEST_CASE=normal
run prepare > /dev/null
reject_writes
cmp "$stage/workspace-images.json" "$test_dir/workspace-images.json"

# An uncertain copy stays private until the same candidate can find that exact image.
saved_cloud=$AMI_TEST_STATE
export AMI_TEST_STATE="$test_dir/hidden-cloud" AMI_TEST_CASE=hidden-copy
mkdir -- "$AMI_TEST_STATE"
reject prepare "$test_dir/hidden-stage"
test -f "$AMI_TEST_STATE/copied-us-west-1"
test ! -e "$test_dir/hidden-stage/workspace-images.json"
if grep -Eq '^ec2 (modify-image-attribute|modify-snapshot-attribute|deregister-image|delete-snapshot) ' "$AMI_TEST_TRACE"; then
    echo 'Ambiguous copy incorrectly triggered sharing or rollback.' >&2
    exit 1
fi
export AMI_TEST_CASE=normal
run prepare "$test_dir/hidden-stage" > /dev/null
test "$(grep -c '^ec2 copy-image ' "$AMI_TEST_TRACE")" -eq 1
grep -Eq '^ec2 copy-image .*--region us-west-2 ' "$AMI_TEST_TRACE"
export AMI_TEST_STATE=$saved_cloud

# Exact frozen metadata cannot be edited to authorize the share step.
jq '.["us-east-1"].catalogSha256 = "changed"' "$stage/workspace-images.json" > "$test_dir/changed.json"
cp "$test_dir/changed.json" "$stage/workspace-images.json"
reject share
reject_writes
cp "$test_dir/workspace-images.json" "$stage/workspace-images.json"

# All regions must verify before a single public permission can be changed.
for case_name in duplicate bad-lineage encrypted-copy blocked-share private-grant denied-share; do
    export AMI_TEST_CASE=$case_name
    if [[ "$case_name" == duplicate ]]; then
        reject prepare
    else
        reject share
    fi
    reject_writes
done

# Share only the four frozen AMIs, and tolerate a lost response already applied by AWS.
export AMI_TEST_CASE=lost-share
run share > /dev/null
test "$(grep '^ec2 modify-image-attribute ' "$AMI_TEST_TRACE" | grep -c ' --dry-run$')" -eq 4
test "$(grep '^ec2 modify-image-attribute ' "$AMI_TEST_TRACE" | grep -cv ' --dry-run$')" -eq 4
for mapping in us-east-1:1 us-east-2:2 us-west-1:3 us-west-2:4; do
    grep -Eq "^ec2 modify-image-attribute .*--region ${mapping%:*} .*--image-id ami-0123456789abcdef${mapping#*:} " "$AMI_TEST_TRACE"
done
export AMI_TEST_CASE=normal
run share > /dev/null
reject_writes
