#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"
export REGISTRY_TEST_DIR
REGISTRY_TEST_DIR=$(mktemp -d)
trap 'rm -rf "$REGISTRY_TEST_DIR"' EXIT
mkdir "$REGISTRY_TEST_DIR/bin"
printf '{"os":"linux","architecture":"amd64"}\n' >"$REGISTRY_TEST_DIR/config.json"
config="sha256:$(manifest_sha256 "$REGISTRY_TEST_DIR/config.json")"
jq -n --arg digest "$config" '{schemaVersion:2,config:{digest:$digest}}' >"$REGISTRY_TEST_DIR/image.json"
image="sha256:$(manifest_sha256 "$REGISTRY_TEST_DIR/image.json")"
jq -n --arg digest "$image" '{schemaVersion:2,manifests:[{digest:$digest,platform:{os:"linux",architecture:"amd64"}}]}' >"$REGISTRY_TEST_DIR/index.json"
index="sha256:$(manifest_sha256 "$REGISTRY_TEST_DIR/index.json")"
export MOCK_CONFIG=$config MOCK_IMAGE=$image MOCK_INDEX=$index
cat >"$REGISTRY_TEST_DIR/bin/curl" <<'CURL'
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
    "https://ghcr.io/v2/jflatten/shiftbox/manifests/$MOCK_INDEX") cp "$REGISTRY_TEST_DIR/index.json" "$destination" ;;
    "https://ghcr.io/v2/jflatten/shiftbox/manifests/$MOCK_IMAGE") cp "$REGISTRY_TEST_DIR/image.json" "$destination" ;;
    "https://ghcr.io/v2/jflatten/shiftbox/blobs/$MOCK_CONFIG")
        if [ "${MOCK_DRIFT:-}" = yes ]; then printf '{"os":"linux","architecture":"arm64"}\n' >"$destination"
        else cp "$REGISTRY_TEST_DIR/config.json" "$destination"; fi ;;
    *) exit 97 ;;
esac
CURL
chmod +x "$REGISTRY_TEST_DIR/bin/curl"
export PATH="$REGISTRY_TEST_DIR/bin:$PATH"

# Both a single manifest and the amd64 member of an OCI index resolve by digest.
manifest_verify_controller ghcr.io/jflatten/shiftbox "$image"
manifest_verify_controller ghcr.io/jflatten/shiftbox "$index"

# A substituted config or foreign repository cannot qualify an image.
if MOCK_DRIFT=yes manifest_verify_controller ghcr.io/jflatten/shiftbox "$image"; then exit 1; fi
if manifest_verify_controller ghcr.io/elsewhere/shiftbox "$image"; then exit 1; fi
printf 'Anonymous OCI verification fixtures passed.\n'
