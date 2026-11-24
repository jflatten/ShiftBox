#!/bin/sh
set -eu
umask 077

installation=/var/lib/shiftbox
test -f "$installation/.persistent-volume" || {
    echo 'ShiftBox requires its mounted installation volume' >&2
    exit 1
}
: "${SHIFTBOX_DB_HOST:?missing private database host}"
: "${SHIFTBOX_CLOUD_CONFIG:?missing cloud configuration}"
: "${SHIFTBOX_APP_PASSWORD:?missing application database password}"

mode=${1:-}
bootstrap_claim=${SHIFTBOX_BOOTSTRAP_CLAIM:-}
unset SHIFTBOX_BOOTSTRAP_CLAIM
runtime="$(mktemp -d)"
github_app_file=
if { [ "$mode" = serve ] || [ "$mode" = one-pass ]; } && [ -n "${SHIFTBOX_GITHUB_APP:-}" ]; then
    github_app_file="$runtime/github-app.json"
    printf '%s' "$SHIFTBOX_GITHUB_APP" > "$github_app_file"
fi
unset SHIFTBOX_GITHUB_APP

ca="$installation/postgres-ca.pem"
tmp_ca="$(mktemp "$installation/.postgres-ca.XXXXXXXX")"
if [ "$mode" = one-pass ]; then
    cat /run/shiftbox/database-trust/ca.crt > "$tmp_ca"
    # Keep the browser trust bundle separate from the database trust used by pgx.
    SSL_CERT_FILE="$runtime/browser-roots.pem"
    cat /etc/ssl/certs/ca-certificates.crt /run/shiftbox/browser-tls/ca.crt > "$SSL_CERT_FILE"
    export SSL_CERT_FILE
    export PGSSLROOTCERT="$ca"
else
    : "${SHIFTBOX_DB_CA:?missing database CA}"
    printf '%s\n' "$SHIFTBOX_DB_CA" > "$tmp_ca"
fi
mv -f "$tmp_ca" "$ca"
export SHIFTBOX_CA_FILE="$ca"

jq -n --arg mode "$mode" --arg githubAppFile "$github_app_file" '
  ($ENV.SHIFTBOX_CA_FILE | @uri) as $ca |
  ($ENV.SHIFTBOX_APP_PASSWORD | @uri) as $password |
  ($ENV.SHIFTBOX_CLOUD_CONFIG | fromjson) + {
    postgresURI: ("postgres://shiftbox_app:" + $password + "@" + $ENV.SHIFTBOX_DB_HOST +
      "/shiftbox?sslmode=verify-full" + (if $mode == "serve" then "&sslrootcert=" + $ca else "" end))
  } | if $githubAppFile == "" then . else .runtime.githubAppFile = $githubAppFile end
' > "$runtime/config.json"

case "$mode" in
    setup|one-pass)
        : "${SHIFTBOX_DEX_PASSWORD:?missing Dex database password}"
        jq -n '
          ($ENV.SHIFTBOX_DEX_PASSWORD | @uri) as $password |
          {dexUri: ("postgres://shiftbox_dex:" + $password + "@" + $ENV.SHIFTBOX_DB_HOST +
            "/shiftbox?sslmode=verify-full"), caFile: $ENV.SHIFTBOX_CA_FILE}
        ' > "$runtime/dex.json"
        unset SHIFTBOX_APP_PASSWORD SHIFTBOX_DEX_PASSWORD
        if [ "$mode" = one-pass ]; then
            # The Go process consumes and removes this private file before
            # invoking Dex or starting the normal controller.
            printf '%s' "$bootstrap_claim" > "$runtime/setup-claim"
            unset bootstrap_claim
            exec shiftbox one-pass "$installation" "$runtime/config.json" "$runtime/dex.json" "$runtime/setup-claim"
        fi
        exec shiftbox setup --installation-dir "$installation" --config "$runtime/config.json" \
            --postgres-identity-config "$runtime/dex.json"
        ;;
    serve)
        unset SHIFTBOX_APP_PASSWORD
        exec shiftbox serve --config "$runtime/config.json"
        ;;
    *)
        echo 'usage: shiftbox-cloud setup | serve | one-pass' >&2
        exit 1
        ;;
esac
