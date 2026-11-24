#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
image=${1:-shiftbox-controller:smoke}
dockerfile="$root/platform/container/Dockerfile"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
helper=${2:-"$scratch/helper"}
if [ "$#" -lt 2 ]; then
    sh "$root/platform/helper/macos/package.sh" --portable "$helper"
fi

# Exercise the release image from a clean context and check that the runtime can
# invoke its managed identity helper and PostgreSQL backup tools as a non-root user.
test -f "$dockerfile"
docker buildx build --load --platform linux/amd64 --file "$dockerfile" \
    --build-context "macos-helper=$helper" --tag "$image" "$root"
docker run --rm --platform linux/amd64 --entrypoint /bin/sh "$image" -ec '
    test "$(uname -m)" = x86_64
    test "$(id -u):$(id -g)" = 10001:10001
    test -w /var/lib/shiftbox

    output=$(shiftbox 2>&1) && exit 1
    case "$output" in
        "usage: shiftbox "*) ;;
        *) printf "%s\n" "$output" >&2; exit 1 ;;
    esac

    dex version | grep -Fq "v2.46.1"
    for tool in psql pg_dump pg_restore; do
        "$tool" --version | grep -Eq "PostgreSQL\) 17\."
    done

    # Exercise the production entry point with a mounted volume and an encoded database password.
    touch /var/lib/shiftbox/.persistent-volume
    fixture=$(mktemp -d)
    printf "%s\n" "#!/bin/sh" "test \"\$1\" = serve || exit 1" \
        "jq -er .postgresURI \"\$3\"" > "$fixture/shiftbox"
    chmod +x "$fixture/shiftbox"
    uri=$(PATH="$fixture:$PATH" SHIFTBOX_DB_HOST=postgres.example.test SHIFTBOX_DB_CA=fixture-ca \
        SHIFTBOX_CLOUD_CONFIG="{\"runtime\":{\"provider\":\"ec2\"}}" SHIFTBOX_APP_PASSWORD="fixture@password" \
        shiftbox-cloud serve)
    case "$uri" in
        "postgres://shiftbox_app:fixture%40password@postgres.example.test/shiftbox?sslmode=verify-full&sslrootcert="*) ;;
        *) exit 1 ;;
    esac
'
