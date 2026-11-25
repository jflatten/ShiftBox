#!/usr/bin/env bash
# Exercise the controller entrypoint without starting ECS or exposing credentials.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
mkdir -p "$scratch/installation" "$scratch/database-trust" "$scratch/browser-tls" "$scratch/runtime" "$scratch/bin"
touch "$scratch/installation/.persistent-volume"
printf 'Database CA fixture\n' >"$scratch/database-trust/ca.crt"
printf 'Private browser CA fixture\n' >"$scratch/browser-tls/ca.crt"
printf 'System CA fixture\n' >"$scratch/system-ca.crt"
sed -e "s|/var/lib/shiftbox|$scratch/installation|g" \
    -e "s|/run/shiftbox/database-trust|$scratch/database-trust|g" \
    -e "s|/run/shiftbox/browser-tls|$scratch/browser-tls|g" \
    -e "s|/etc/ssl/certs/ca-certificates.crt|$scratch/system-ca.crt|g" \
    "$here/cloud-start.sh" >"$scratch/cloud-start.sh"
cat >"$scratch/bin/shiftbox" <<'FAKE'
#!/bin/sh
set -eu
expected='postgres://shiftbox_app:app-fixture@postgres.test.internal/shiftbox?sslmode=verify-full'
case "$1" in
    one-pass)
        test "$PGSSLROOTCERT" = "$SHIFTBOX_TEST_CA"
        jq -e --arg expected "$expected" '.postgresURI == $expected' "$3" >/dev/null
        jq -e --arg ca "$SHIFTBOX_TEST_CA" '.caFile == $ca and (.dexUri | endswith("?sslmode=verify-full"))' "$4" >/dev/null
        ;;
    setup)
        jq -e --arg expected "$expected" '.postgresURI == $expected' "$5" >/dev/null
        ;;
    serve)
        jq -e --arg expected "$expected" '.postgresURI | startswith($expected + "&sslrootcert=")' "$3" >/dev/null
        ;;
    *) exit 1 ;;
esac
FAKE
chmod +x "$scratch/bin/shiftbox"
export PATH="$scratch/bin:$PATH" TMPDIR="$scratch/runtime" SHIFTBOX_TEST_CA="$scratch/installation/postgres-ca.pem"
export SHIFTBOX_CLOUD_CONFIG='{}' SHIFTBOX_DB_HOST=postgres.test.internal SHIFTBOX_APP_PASSWORD=app-fixture
export SHIFTBOX_DEX_PASSWORD=dex-fixture SHIFTBOX_BOOTSTRAP_CLAIM=claim-fixture SHIFTBOX_DB_CA='Database CA fixture'

# Both one-pass setups supply the CA separately. The setup URI validator only accepts sslmode.
SHIFTBOX_ACME_EMAIL='' sh "$scratch/cloud-start.sh" one-pass
SHIFTBOX_ACME_EMAIL='operator@example.com' sh "$scratch/cloud-start.sh" one-pass

# Legacy setup still supplies the CA separately, and the serve-only configuration specifies it in the URI.
sh "$scratch/cloud-start.sh" setup
sh "$scratch/cloud-start.sh" serve
