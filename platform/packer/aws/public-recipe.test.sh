#!/bin/sh
set -eu

recipe=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
fixture="$test_dir/source"
mkdir -p "$fixture/platform/packer/aws" "$test_dir/bin"
cp "$recipe/build-image.sh" "$fixture/platform/packer/aws/build-image.sh"
printf '.cache/\n' > "$fixture/.gitignore"
git -C "$fixture" init -q
git -C "$fixture" add .
git -C "$fixture" -c user.name=Fixture -c user.email=fixture@example.test \
    -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm fixture

export FIXTURE_TRACE="$test_dir/trace" FIXTURE_SOURCE=ami-0123456789abcdef0 FIXTURE_OUTPUT=ami-0123456789abcdef1
export FIXTURE_ACCOUNT=111111111111 FIXTURE_REGION=us-east-2 FIXTURE_RELEASE=v1.2.3-rc.4
export FIXTURE_REVISION
FIXTURE_REVISION=$(git -C "$fixture" rev-parse HEAD)
export PATH="$test_dir/bin:$PATH"
export GO="$test_dir/bin/stub-go" PACKER="$test_dir/bin/stub-packer"
dd if=/dev/zero of="$test_dir/key.pub" bs=32 count=1 2>/dev/null
jq -cn --arg region "$FIXTURE_REGION" --arg ami "$FIXTURE_SOURCE" \
    '{region:$region,source_ami:$ami}' > "$test_dir/publisher.pkrvars.json"
cat > "$test_dir/bin/aws" <<'AWS'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FIXTURE_TRACE"
case "$1 $2" in
    'sts get-caller-identity')
        if [ "$FIXTURE_CASE" = wrong-publisher ]; then echo 222222222222; else echo "$FIXTURE_ACCOUNT"; fi
        ;;
    'ec2 get-ebs-encryption-by-default')
        if [ "$FIXTURE_CASE" = encrypted-default ]; then echo True; else echo False; fi
        ;;
    'ec2 describe-images')
        case "$*" in
            *"$FIXTURE_SOURCE"*) ami=$FIXTURE_SOURCE; owner=099720109477; public=true ;;
            *"$FIXTURE_OUTPUT"*) ami=$FIXTURE_OUTPUT; owner=$FIXTURE_ACCOUNT; public=false ;;
            *) exit 2 ;;
        esac
        encrypted=false
        if [ "$ami" = "$FIXTURE_SOURCE" ]; then
            if [ "$FIXTURE_CASE" = foreign-source ]; then owner=333333333333; fi
            if [ "$FIXTURE_CASE" = encrypted-source ]; then encrypted=true; fi
        fi
        if [ "$ami" = "$FIXTURE_OUTPUT" ] && [ "$FIXTURE_CASE" = public-output ]; then public=true; fi
        jq -cn --arg ami "$ami" --arg owner "$owner" --argjson public "$public" --argjson encrypted "$encrypted" '
        {Images:[{ImageId:$ami,OwnerId:$owner,Public:$public,State:"available",Architecture:"x86_64",
          RootDeviceName:"/dev/sda1",RootDeviceType:"ebs",VirtualizationType:"hvm",
          Name:"ubuntu-minimal/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-minimal-fixture",
          BlockDeviceMappings:[{DeviceName:"/dev/sda1",Ebs:{SnapshotId:"snap-0123456789abcdef0",Encrypted:$encrypted,VolumeSize:32}}],
          ProductCodes:[],Tags:[{Key:"ShiftBoxArtifact",Value:"workspace-ami"},
            {Key:"ShiftBoxRelease",Value:env.FIXTURE_RELEASE},{Key:"ShiftBoxSourceAMI",Value:env.FIXTURE_SOURCE},
            {Key:"ShiftBoxSourceCommit",Value:env.FIXTURE_REVISION}]}]}'
        ;;
    'ec2 describe-snapshots')
        encrypted=false
        if [ "$FIXTURE_CASE" = encrypted-output-snapshot ]; then encrypted=true; fi
        jq -cn --argjson encrypted "$encrypted" '
        {Snapshots:[{SnapshotId:"snap-0123456789abcdef0",OwnerId:env.FIXTURE_ACCOUNT,Encrypted:$encrypted,State:"completed",VolumeSize:32}]}'
        ;;
    *) printf 'Unexpected AWS operation: %s\n' "$*" >&2; exit 2 ;;
esac
AWS
cat > "$test_dir/bin/stub-go" <<'GO'
#!/bin/sh
set -eu
printf 'GO %s\n' "$*" >> "$FIXTURE_TRACE"
while [ "$1" != -o ]; do shift; done
printf 'guest fixture' > "$2"
GO
cat > "$test_dir/bin/stub-packer" <<'PACKER'
#!/bin/sh
set -eu
printf 'PACKER %s\n' "$*" >> "$FIXTURE_TRACE"
if [ "$1" = validate ]; then
    [ "$FIXTURE_CASE" != invalid-packer-template ]
    exit
fi
for argument; do
    case "$argument" in
        manifest_path=*) manifest=${argument#manifest_path=} ;;
        agent_sha256=*) agent=${argument#agent_sha256=} ;;
        guest_update_key_sha256=*) update_key=${argument#guest_update_key_sha256=} ;;
    esac
done
run=00000000-0000-4000-8000-000000000001
source_region=$FIXTURE_REGION
if [ "$FIXTURE_CASE" = wrong-source-region ]; then source_region=us-east-1; fi
jq -cn --arg run "$run" --arg sourceRegion "$source_region" --arg agent "$agent" --arg updateKey "$update_key" '
{last_run_uuid:$run,builds:[{builder_type:"amazon-ebs",packer_run_uuid:$run,artifact_id:(env.FIXTURE_REGION+":"+env.FIXTURE_OUTPUT),
 custom_data:{provider:"ec2",publicBuild:"true",sourceRegion:$sourceRegion,region:env.FIXTURE_REGION,publisherAccountId:env.FIXTURE_ACCOUNT,
 sourceAmiId:env.FIXTURE_SOURCE, sourceAmiOwnerId:"099720109477",sourceRevision:env.FIXTURE_REVISION,
 releaseId:env.FIXTURE_RELEASE,guestAgentRelease:env.FIXTURE_RELEASE,guestAgentSHA256:$agent,
 guestUpdateKeySHA256:$updateKey,architecture:"amd64",minimumRootDiskGiB:"32"}}]}' > "$manifest"
PACKER
chmod +x "$test_dir/bin/"*

build() {
    FIXTURE_CASE=$1
    export FIXTURE_CASE
    : > "$FIXTURE_TRACE"
    PUBLIC_AMI_BUILD=$2 RELEASE_CANDIDATE=$FIXTURE_RELEASE RELEASE_PUBLISHER_ACCOUNT_ID=$FIXTURE_ACCOUNT \
        GUEST_AGENT_RELEASE='' "$fixture/platform/packer/aws/build-image.sh" "$FIXTURE_RELEASE" "$test_dir/key.pub" \
        "$test_dir/publisher.pkrvars.json" > "$test_dir/build.out" 2>&1
}

# Public-mode checks fail before Packer when the approved identity or clean base is wrong.
for scenario in wrong-publisher encrypted-default foreign-source encrypted-source; do
    if build "$scenario" 1 || grep -qE '^GO |^PACKER ' "$FIXTURE_TRACE"; then
        printf 'Public build started with rejected fixture %s.\n' "$scenario" >&2
        exit 1
    fi
done

# Exact build and base regions, publisher and resulting private artifact survive handoff.
build valid 1
grep -Fq -- '-var public_build=true' "$FIXTURE_TRACE"
grep -Fq -- "-var publisher_account_id=$FIXTURE_ACCOUNT" "$FIXTURE_TRACE"
grep -Fq "AMI provenance manifest:" "$test_dir/build.out"

# Static Packer validation runs before build and a failed recipe never launches a guest.
test "$(grep '^PACKER ' "$FIXTURE_TRACE" | awk '{print $2}' | paste -sd ' ' -)" = 'validate build'
if build invalid-packer-template 1 || grep -q '^PACKER build ' "$FIXTURE_TRACE"; then
    echo 'A rejected Packer validation reached the build command.' >&2
    exit 1
fi

# Rejected output never receives a success handoff or a publication API call.
for scenario in wrong-source-region public-output encrypted-output-snapshot; do
    if build "$scenario" 1 || grep -Fq 'AMI provenance manifest:' "$test_dir/build.out"; then
        printf 'Public build accepted rejected artifact fixture %s.\n' "$scenario" >&2
        exit 1
    fi
    if grep -Eq '^ec2 (modify-image-attribute|copy-image|modify-snapshot-attribute) ' "$FIXTURE_TRACE"; then
        echo 'Builder attempted to publish or copy an image.' >&2
        exit 1
    fi
done

# A private build still uses the private recipe with no public preflight AWS calls.
build valid 0
grep -Fq -- '-var public_build=false' "$FIXTURE_TRACE"
if grep -Eq '^(sts|ec2) ' "$FIXTURE_TRACE"; then
    echo 'Private build used public AWS preflight.' >&2
    exit 1
fi

# The public cleanliness check reads only the fixture root. Never invoke sealing on the test host.
grep -Fq 'verify-public)' "$recipe/seal-image.sh"
image_root="$test_dir/image-root"
mkdir -p "$image_root/etc" "$image_root/root/.aws"
printf 'root:!:0:0:99999:7:::\nubuntu:!:0:0:99999:7:::\n' > "$image_root/etc/shadow"
printf 'uninitialized\n' > "$image_root/etc/machine-id"
rmdir "$image_root/root/.aws"
sh "$recipe/seal-image.sh" verify-public "$image_root"
mkdir "$image_root/root/.aws"
printf 'credential fixture, not a real key\n' > "$image_root/root/.aws/credentials"
if sh "$recipe/seal-image.sh" verify-public "$image_root" > "$test_dir/seal.out" 2>&1; then
    echo 'Verifier accepted a credential file in a public image.' >&2
    exit 1
fi
if grep -Fq 'credential fixture' "$test_dir/seal.out"; then
    echo 'Verifier disclosed file contents.' >&2
    exit 1
fi
