#!/usr/bin/env bash
# Exercise the hosted Packer handoff with no AWS or GitHub calls.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir -p "$fixture/platform/packer/aws" "$fixture/platform/release" "$fixture/job"
cp "$here/hosted-build.sh" "$fixture/platform/packer/aws/"
export RUNNER_TEMP="$fixture/job" FIXTURE_DIR="$fixture"
ssh-keygen -q -t ed25519 -N '' -f "$fixture/build-key"
export SHIFTBOX_RELEASE_BUILD_SSH_KEY
SHIFTBOX_RELEASE_BUILD_SSH_KEY=$(cat "$fixture/build-key")
digest=$(awk '{print $2}' "$fixture/build-key.pub" | tr -d '\n' | shasum -a 256)
export SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY
SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY=$(head -c 32 /dev/zero | openssl base64 -A)
deadline=$(python3 -c 'from datetime import datetime,timedelta,timezone; print((datetime.now(timezone.utc)+timedelta(minutes=100)).strftime("%Y-%m-%dT%H:%M:%SZ"))')
export SHIFTBOX_RELEASE_BUILD_NETWORK
SHIFTBOX_RELEASE_BUILD_NETWORK=$(jq -cn --arg deadline "$deadline" --arg digest "${digest%% *}" '
  {account:"668112512962",region:"us-east-2",candidate_tag:"v1.0.0-rc.1",attempt_id:"fixture",
   vpc_id:"vpc-00000000000000001",private_subnet_id:"subnet-00000000000000002",private_subnet_cidr:"10.98.0.64/26",
   builder_group_id:"sg-00000000000000003",endpoint_id:"eice-00000000000000004",source_ami:"ami-00000000000000005",
   packer_key_pair:"shiftbox-publisher-fixture-packer",packer_public_key_sha256:$digest,deadline:$deadline}')
approved_network=$SHIFTBOX_RELEASE_BUILD_NETWORK

cat > "$fixture/platform/packer/aws/eice-socks.py" <<'PY'
import os
import pathlib
import sys
import time

assert "SHIFTBOX_RELEASE_BUILD_SSH_KEY" not in os.environ
assert sys.argv[sys.argv.index("--attempt-id") + 1] == "fixture"
assert sys.argv[sys.argv.index("--subnet-cidr") + 1] == "10.98.0.64/26"
port_file = pathlib.Path(sys.argv[sys.argv.index("--port-file") + 1])
assert port_file.parent.stat().st_mode & 0o777 == 0o700
port_file.write_text("4125\n")
time.sleep(120)
PY
cat > "$fixture/platform/packer/aws/build-image.sh" <<'PACKER'
#!/usr/bin/env bash
set -euo pipefail
test "$PUBLIC_AMI_BUILD" = 1
test "$RELEASE_CANDIDATE" = "$1"
test -z "${SHIFTBOX_RELEASE_BUILD_SSH_KEY+x}"
test "$(wc -c < "$2" | tr -d ' ')" = 32
jq -e '
  .publisher_attempt=="fixture" and .environment=="release-v1.0.0-rc.1" and .build_ssh_proxy_host=="127.0.0.1" and
  .build_ssh_proxy_port==4125 and .build_subnet_id=="subnet-00000000000000002"
' "$3" >/dev/null
test -s "$(jq -er .build_private_key_file "$3")"
touch "$FIXTURE_DIR/packer-called"
printf 'AMI provenance manifest: %s\n' "$FIXTURE_DIR/manifest.json"
PACKER
cat > "$fixture/platform/release/amis.sh" <<'AMI'
#!/usr/bin/env bash
set -euo pipefail
test "$1" = packer-handoff
test "$5" = "$FIXTURE_DIR/manifest.json"
printf '{"sanitized":true}\n'
AMI
chmod 700 "$fixture/platform/packer/aws/build-image.sh" "$fixture/platform/release/amis.sh"
hosted_build() {
    bash "$fixture/platform/packer/aws/hosted-build.sh" v1.0.0-rc.1 0123456789abcdef0123456789abcdef01234567 \
        668112512962 "$RUNNER_TEMP/handoff.json"
}

# Invalid network identity, stale deadlines, and mismatched key pins fail before creating a build.
for edit in '.account="000000000000"' '.deadline="2020-01-01T00:00:00Z"' \
    '.packer_key_pair="unrelated"' '.packer_public_key_sha256=("0"*64)' '.extra="unexpected"'; do
    SHIFTBOX_RELEASE_BUILD_NETWORK=$(jq -c "$edit" <<< "$approved_network")
    if hosted_build >"$fixture/error" 2>&1; then
        echo "Hosted fixture accepted invalid network data: $edit" >&2
        exit 1
    fi
    test ! -e "$fixture/packer-called"
    test -z "$(ls -A "$RUNNER_TEMP")"
    if grep -Fq 'BEGIN OPENSSH PRIVATE KEY' "$fixture/error"; then
        echo 'Private build key reached the fixture log.' >&2
        exit 1
    fi
done

# A valid build uses loopback and emits only sanitized metadata, with secrets and the bridge removed.
SHIFTBOX_RELEASE_BUILD_NETWORK=$approved_network
hosted_build
jq -e '.sanitized == true' "$RUNNER_TEMP/handoff.json" >/dev/null
test -f "$fixture/packer-called"
test "$(ls -A "$RUNNER_TEMP")" = handoff.json
printf 'Hosted Packer input and handoff fixtures passed.\n'
