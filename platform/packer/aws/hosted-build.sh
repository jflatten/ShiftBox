#!/usr/bin/env bash
# Run Packer on a hosted VM using only the approved disposable Ohio attempt.
set -euo pipefail
umask 077

if [[ $# != 4 && ( $# != 5 || ${5:-} != --rehearsal ) ]]; then
  printf 'Usage: %s <rc-tag> <source-commit> <publisher-account> <output> [--rehearsal]\n' "$0" >&2
  exit 2
fi
tag=$1 source_commit=$2 publisher=$3 handoff=$4
private_key=${SHIFTBOX_RELEASE_BUILD_SSH_KEY:?}
unset SHIFTBOX_RELEASE_BUILD_SSH_KEY
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d "${RUNNER_TEMP:?}/shiftbox-ami.XXXXXX")
bridge_pid=''
cleanup() {
  if [[ -n "$bridge_pid" ]]; then
    kill "$bridge_pid" 2>/dev/null || true
    wait "$bridge_pid" 2>/dev/null || true
  fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'Hosted Packer build: %s\n' "$1" >&2
  exit 1
}

network="$scratch/network.json"
if ! jq -ces --arg tag "$tag" --arg publisher "$publisher" '
  select(length==1) | .[0] |
  select(keys==["account","attempt_id","builder_group_id","candidate_tag","deadline","endpoint_id","packer_key_pair",
    "packer_public_key_sha256","private_subnet_cidr","private_subnet_id","region","source_ami","vpc_id"]) |
  select(($publisher|test("^[0-9]{12}$")) and .account==$publisher and .region=="us-east-2" and .candidate_tag==$tag) |
  select((.attempt_id|test("^[a-z0-9][a-z0-9-]{0,17}$")) and
    .packer_key_pair==("shiftbox-publisher-"+.attempt_id+"-packer") and
    (.packer_public_key_sha256|test("^[a-f0-9]{64}$")) and
    (.private_subnet_cidr|test("^[0-9]{1,3}(\\.[0-9]{1,3}){2}\\.64/26$")) and
    (.vpc_id|test("^vpc-[a-f0-9]{17}$")) and (.private_subnet_id|test("^subnet-[a-f0-9]{17}$")) and
    (.builder_group_id|test("^sg-[a-f0-9]{17}$")) and (.endpoint_id|test("^eice-[a-f0-9]{17}$")) and
    (.source_ami|test("^ami-[a-f0-9]{17}$"))) |
  select((.deadline | fromdateiso8601)-now >= 3000 and (.deadline | fromdateiso8601)-now <= 7200)
' <<< "${SHIFTBOX_RELEASE_BUILD_NETWORK:-}" > "$network" 2>/dev/null; then
  fail 'use the exact reviewed Ohio attempt inputs with 50–120 minutes remaining'
fi
attempt=$(jq -er .attempt_id "$network")
if [[ $# == 5 ]]; then
  [[ "$attempt" == "$REHEARSAL_ATTEMPT" && "$attempt" =~ ^rehearsal-[a-z0-9]{1,8}$ ]] ||
    fail 'the rehearsal must use its exact reviewed attempt'
  [[ "$(git -C "$here" rev-parse HEAD)" == "$source_commit" ]] || fail 'the reviewed rehearsal commit changed'
else
  [[ "$attempt" != rehearsal-* ]] || fail 'the release cannot consume a rehearsal attempt'
fi

key="$scratch/build-key"
printf '%s\n' "$private_key" > "$key"
unset private_key
public_key=$(ssh-keygen -y -P '' -f "$key" 2>/dev/null) || fail 'the attempt SSH key is invalid'
read -r key_type key_data _ <<< "$public_key"
key_digest=$(printf %s "$key_data" | shasum -a 256)
[[ "$key_type" == ssh-ed25519 && "${key_digest%% *}" == "$(jq -er .packer_public_key_sha256 "$network")" ]] ||
  fail 'the SSH key does not match the reviewed attempt'
guest_key="$scratch/guest-update.pub"
[[ "${SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY:-}" =~ ^[A-Za-z0-9+/]{43}=$ ]] ||
  fail 'provide the single-line base64 guest-update public key'
printf %s "$SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY" | openssl base64 -A -d > "$guest_key"
[[ "$(wc -c < "$guest_key" | tr -d ' ')" == 32 ]] || fail 'the guest-update public key must be 32 bytes'

python3 "$here/eice-socks.py" --account-id "$publisher" --candidate-tag "$tag" \
  --endpoint-id "$(jq -er .endpoint_id "$network")" --vpc-id "$(jq -er .vpc_id "$network")" \
  --subnet-id "$(jq -er .private_subnet_id "$network")" --subnet-cidr "$(jq -er .private_subnet_cidr "$network")" \
  --security-group-id "$(jq -er .builder_group_id "$network")" --attempt-id "$(jq -er .attempt_id "$network")" \
  --port-file "$scratch/eice.port" &
bridge_pid=$!
for ((check=0;check<100;check++)); do
  [[ ! -s "$scratch/eice.port" ]] || break
  kill -0 "$bridge_pid" 2>/dev/null || fail 'the EICE bridge rejected the attempt'
  sleep 0.2
done
[[ -s "$scratch/eice.port" ]] || fail 'the EICE bridge did not become ready'
port=$(cat "$scratch/eice.port")
if [[ ! "$port" =~ ^[1-9][0-9]{0,4}$ ]] || ((port > 65535)); then
  fail 'the EICE bridge returned an invalid port'
fi

vars="$scratch/build.pkrvars.json"
jq --arg key "$key" --argjson port "$port" '
  {region,source_ami,build_subnet_id:.private_subnet_id,build_security_group_id:.builder_group_id,
   cluster_name:"shiftbox-publisher",environment:("release-"+.candidate_tag),publisher_attempt:.attempt_id,
   build_key_pair:.packer_key_pair,build_private_key_file:$key,
   build_ssh_proxy_host:"127.0.0.1",build_ssh_proxy_port:$port}
' "$network" > "$vars"
if ! PUBLIC_AMI_BUILD=1 RELEASE_CANDIDATE="$tag" RELEASE_PUBLISHER_ACCOUNT_ID="$publisher" \
  "$here/build-image.sh" "$tag" "$guest_key" "$vars" > "$scratch/packer.log" 2>&1; then
  # The public job may report these fixed stage names, never the underlying build log.
  checkpoint=$(sed -nE 's/^([[:blank:]]*amazon-ebs\.shiftbox: )?ShiftBox Packer checkpoint: (publisher-preflight|guest-binary|recipe-validation|packer-build|guest-bootstrap|guest-toolchain|guest-agent|guest-seal|ami-creation|ami-verification)$/\2/p' \
    "$scratch/packer.log" | tail -n 1)
  fail "Packer failed at ${checkpoint:-startup}. Check the scoped AWS attempt and reconcile any private artifacts before retrying."
fi
manifest=$(sed -n 's/^AMI provenance manifest: //p' "$scratch/packer.log")
if [[ $# == 5 ]]; then
  # The verified rehearsal output is intentionally incompatible with an RC handoff.
  jq -e '{result:"private-rehearsal-built",count:(.builds|length)} | select(.count==1)' "$manifest" > "$handoff"
else
  "$here/../../release/amis.sh" packer-handoff "$tag" "$source_commit" "$publisher" "$manifest" > "$handoff"
fi
