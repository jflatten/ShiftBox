#!/bin/sh
set -eu

recipe=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
packer=${PACKER:-packer}
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT

# Validate the pinned builder configuration without launching an EC2 instance.
printf 'validation-only guest agent' > "$test_dir/shiftbox-agent"
ssh-keygen -q -t rsa -N '' -f "$test_dir/build-key"
dd if=/dev/zero of="$test_dir/guest-update.pub" bs=32 count=1 2>/dev/null
cat > "$test_dir/release.pkrvars.hcl" <<VARS
region                  = "us-east-1"
source_ami              = "ami-0123456789abcdef0"
build_subnet_id         = "subnet-0123456789abcdef0"
build_security_group_id = "sg-0123456789abcdef0"
cluster_name            = "recipe-test"
build_key_pair          = "recipe-test"
build_private_key_file  = "$test_dir/build-key"
release_id              = "test-amd64"
source_revision         = "validation-only"
agent_binary            = "$test_dir/shiftbox-agent"
agent_sha256            = "0000000000000000000000000000000000000000000000000000000000000000"
guest_update_public_key = "$test_dir/guest-update.pub"
guest_update_key_sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
manifest_path           = "$test_dir/manifest.json"
VARS

"$packer" fmt -check "$recipe"
"$packer" validate -evaluate-datasources=false -var-file="$test_dir/release.pkrvars.hcl" "$recipe"

# Reject an image alias so the builder always starts from an immutable AMI ID.
if "$packer" validate -evaluate-datasources=false -var-file="$test_dir/release.pkrvars.hcl" \
    -var 'source_ami=latest' "$recipe" > "$test_dir/invalid.out" 2>&1; then
    echo "Packer accepted an unpinned source AMI." >&2
    exit 1
fi
grep -F 'source_ami must be a pinned AMI ID' "$test_dir/invalid.out"
