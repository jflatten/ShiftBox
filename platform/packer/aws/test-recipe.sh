#!/bin/sh
set -eu

recipe=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
packer=${PACKER:-packer}
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT

test_build_identity() {
    fixture="$test_dir/source"
    trace="$test_dir/build-trace"
    mkdir -p "$fixture/platform/packer/aws"
    cp "$recipe/build-image.sh" "$fixture/platform/packer/aws/build-image.sh"
    printf '.cache/\n' > "$fixture/.gitignore"
    git -C "$fixture" init -q
    git -C "$fixture" add .gitignore platform/packer/aws/build-image.sh
    git -C "$fixture" -c user.name=Fixture -c user.email=fixture@example.test \
        -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm fixture
    dd if=/dev/zero of="$test_dir/build-identity.pub" bs=32 count=1 2>/dev/null
    touch "$test_dir/build-identity.pkrvars.hcl"
    cat > "$test_dir/stub-go" <<'GO'
#!/bin/sh
set -eu
printf '%s\n' "$@" >> "$BUILD_TEST_TRACE"
while [ "$1" != -o ]; do shift; done
printf 'guest fixture' > "$2"
GO
    cat > "$test_dir/stub-packer" <<'PACKER'
#!/bin/sh
set -eu
printf '%s\n' "$@" >> "$BUILD_TEST_TRACE"
PACKER
    chmod +x "$test_dir/stub-go" "$test_dir/stub-packer"

    # A clean RC stamps the same candidate in the guest binary and Packer fields.
    candidate=v1.2.3-rc.4
    RELEASE_CANDIDATE=$candidate GUEST_AGENT_RELEASE='' run_fixture_build "$candidate"
    grep -Fxq -- "-X main.agentRelease=$candidate" "$trace"
    grep -Fxq "release_id=$candidate" "$trace"
    grep -Fxq "guest_agent_release=$candidate" "$trace"

    # Private builds keep their original default and explicit guest override.
    RELEASE_CANDIDATE='' GUEST_AGENT_RELEASE='' run_fixture_build development
    grep -Fxq -- '-X main.agentRelease=development' "$trace"
    grep -Fxq 'guest_agent_release=development' "$trace"
    RELEASE_CANDIDATE='' GUEST_AGENT_RELEASE=previous-dev run_fixture_build demo-next
    grep -Fxq 'release_id=demo-next' "$trace"
    grep -Fxq -- '-X main.agentRelease=previous-dev' "$trace"
    grep -Fxq 'guest_agent_release=previous-dev' "$trace"

    # Public builds reject stable tags, malformed RC versions and mismatched identities.
    RELEASE_CANDIDATE=v1.2.3 GUEST_AGENT_RELEASE='' expect_fixture_rejection v1.2.3
    RELEASE_CANDIDATE=v01.2.3-rc.4 GUEST_AGENT_RELEASE='' expect_fixture_rejection v01.2.3-rc.4
    RELEASE_CANDIDATE=v1.2.3-rc.0 GUEST_AGENT_RELEASE='' expect_fixture_rejection v1.2.3-rc.0
    RELEASE_CANDIDATE=$candidate GUEST_AGENT_RELEASE='' expect_fixture_rejection v1.2.3-rc.5
    RELEASE_CANDIDATE=$candidate GUEST_AGENT_RELEASE=v1.2.3-rc.5 expect_fixture_rejection "$candidate"

    # Public builds cannot record a dirty provenance revision against an official candidate.
    printf '\n' >> "$fixture/platform/packer/aws/build-image.sh"
    RELEASE_CANDIDATE=$candidate GUEST_AGENT_RELEASE='' expect_fixture_rejection "$candidate"
}

run_fixture_build() {
    : > "$trace"
    GO="$test_dir/stub-go" PACKER="$test_dir/stub-packer" BUILD_TEST_TRACE="$trace" \
        "$fixture/platform/packer/aws/build-image.sh" "$1" \
        "$test_dir/build-identity.pub" "$test_dir/build-identity.pkrvars.hcl" > "$test_dir/identity.out" 2>&1
}

expect_fixture_rejection() {
    if run_fixture_build "$1" || [ -s "$trace" ]; then
        echo "Build accepted or started for release $1, candidate $RELEASE_CANDIDATE, guest ${GUEST_AGENT_RELEASE:-default}." >&2
        exit 1
    fi
}

test_build_identity
if [ "${1:-}" = --build-identity-only ]; then
    exit 0
fi

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

# Exercise the optional public builder shape without AWS calls or provisioning.
"$packer" validate -evaluate-datasources=false -var-file="$test_dir/release.pkrvars.hcl" \
    -var public_build=true -var publisher_account_id=111111111111 "$recipe"

# Reject an image alias so the builder always starts from an immutable AMI ID.
if "$packer" validate -evaluate-datasources=false -var-file="$test_dir/release.pkrvars.hcl" \
    -var 'source_ami=latest' "$recipe" > "$test_dir/invalid.out" 2>&1; then
    echo "Packer accepted an unpinned source AMI." >&2
    exit 1
fi
grep -F 'source_ami must be a pinned AMI ID' "$test_dir/invalid.out"
