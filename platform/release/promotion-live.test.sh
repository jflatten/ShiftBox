#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"
PROOF_TEST=$(mktemp -d)
export PROOF_TEST
trap 'rm -rf "$PROOF_TEST"' EXIT
mkdir "$PROOF_TEST/bin"
printf 'Resources: {}\n' >"$PROOF_TEST/template.yaml"
printf '{"os":"linux","architecture":"amd64"}\n' >"$PROOF_TEST/oci-config.json"
config="sha256:$(manifest_sha256 "$PROOF_TEST/oci-config.json")"
jq -n --arg digest "$config" '{schemaVersion:2,config:{digest:$digest}}' >"$PROOF_TEST/oci-manifest.json"
image="sha256:$(manifest_sha256 "$PROOF_TEST/oci-manifest.json")"
hash=$(manifest_sha256 "$PROOF_TEST/template.yaml")
jq -n --arg tag v1.4.2-rc.1 --arg commit 0123456789abcdef0123456789abcdef01234567 \
  --arg publisher 111122223333 --arg template "$hash" --arg installer "$hash" --arg support "$hash" \
  --argjson catalog "$(jq -cn --arg h "$hash" '{"us-east-1":$h,"us-east-2":$h,"us-west-1":$h,"us-west-2":$h}')" \
  -f "$here/tests/distribution-fixture.jq" | jq --arg digest "$image" '.controller.digest = $digest' >"$PROOF_TEST/distribution.json"

cat >"$PROOF_TEST/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -eu
destination=''
url=''
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output) destination=$2; shift ;;
        https://*) url=$1 ;;
    esac
    shift
done
case "$url" in
    https://ghcr.io/token*) printf '{"token":"anonymous.fixture"}\n' >"$destination" ;;
    https://ghcr.io/v2/jflatten/shiftbox/manifests/*) cp "$PROOF_TEST/oci-manifest.json" "$destination" ;;
    https://ghcr.io/v2/jflatten/shiftbox/blobs/*) cp "$PROOF_TEST/oci-config.json" "$destination" ;;
    https://*.s3.*.amazonaws.com/public/templates/*)
        if [[ "$url" == *".s3.${TEST_HOSTED_DRIFT:-never}.amazonaws.com/"* ]]; then
            printf 'different hosted template\n' >"$destination"
        else
            cp "$PROOF_TEST/template.yaml" "$destination"
        fi ;;
    *) exit 97 ;;
esac
CURL
cat >"$PROOF_TEST/bin/aws" <<'AWS'
#!/usr/bin/env bash
set -eu
region=$2
case "$3:$4" in
    sts:get-caller-identity) printf '%s\n' "${TEST_PUBLISHER:-111122223333}" ;;
    ec2:describe-images)
        jq -n --slurpfile release "$PROOF_TEST/distribution.json" --arg region "$region" \
          --arg withdrawn "${TEST_WITHDRAWN:-}" --arg deprecated "${TEST_DEPRECATED:-}" '
        $release[0] as $release | $release.regions[$region].workspaceImage as $image |
        {Images:[{OwnerId:$release.publisherAccountId,ImageId:$image.amiId,State:"available",
          Public:($withdrawn != $region),Architecture:"x86_64",VirtualizationType:"hvm",RootDeviceType:"ebs",
          RootDeviceName:$image.rootDeviceName,BlockDeviceMappings:[
            {DeviceName:$image.rootDeviceName,Ebs:{SnapshotId:$image.rootSnapshotId,Encrypted:false}}
          ],DeprecationTime:(if $deprecated == $region then "2000-01-01T00:00:00.000Z" else null end)}]}' ;;
    ec2:describe-image-attribute)
        printf '{"LaunchPermissions":[{"Group":"all"}]}\n' ;;
    ec2:describe-snapshots)
        jq -n --slurpfile release "$PROOF_TEST/distribution.json" --arg region "$region" '
        $release[0] as $release | {Snapshots:[{OwnerId:$release.publisherAccountId,
          SnapshotId:$release.regions[$region].workspaceImage.rootSnapshotId,State:"completed",Encrypted:false}]}' ;;
    *) printf 'Unexpected mutating or unrelated AWS operation %s %s\n' "$3" "$4" >&2; exit 97 ;;
esac
printf '%s %s %s\n' "$region" "$3" "$4" >>"$PROOF_TEST/reads"
AWS
chmod +x "$PROOF_TEST/bin/curl" "$PROOF_TEST/bin/aws"
export PATH="$PROOF_TEST/bin:$PATH"
verify() {
    "$here/promote.sh" verify-live "$PROOF_TEST/distribution.json" "$PROOF_TEST/template.yaml"
}

# A stable candidate checks the real hosted bytes and all regional image observations.
verify
test "$(grep -c 'ec2 describe-images' "$PROOF_TEST/reads")" = 4

# Loss of public visibility, deprecation and hosted drift invalidate promotion.
if TEST_WITHDRAWN=us-west-1 verify >"$PROOF_TEST/reject.log" 2>&1; then exit 1; fi
if TEST_DEPRECATED=us-east-1 verify >"$PROOF_TEST/reject.log" 2>&1; then exit 1; fi
if TEST_HOSTED_DRIFT=us-east-2 verify >"$PROOF_TEST/reject.log" 2>&1; then exit 1; fi
if TEST_PUBLISHER=555544443333 verify >"$PROOF_TEST/reject.log" 2>&1; then exit 1; fi
printf 'Read-only stable promotion preflight fixtures passed.\n'
