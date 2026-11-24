#!/bin/sh
# Run after the retained volume is mounted and before the ECS agent starts.
set -eu
umask 077
root=$1
suffix=$2
region=$3
trust_secret=$4
browser="$root/browser-identity"
postgres="$root/pg-identity"
database_trust="$root/database-trust"
command -v openssl >/dev/null
command -v aws >/dev/null

# Publish each identity directory atomically. Partial generation leaves no
# consumer-visible files. A published identity is never silently regenerated.
issue() (
    target=$1
    common_name=$2
    names=$3
    if [ -d "$target" ]; then
        return
    fi
    scratch=$(mktemp -d "$root/.tls.XXXXXXXX")
    trap 'rm -rf -- "$scratch"' EXIT
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 730 \
        -subj "/CN=ShiftBox $common_name installation CA" \
        -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
        -addext 'keyUsage=critical,keyCertSign,cRLSign' -keyout "$scratch/ca.key" -out "$scratch/ca.crt"
    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -subj "/CN=$common_name" -keyout "$scratch/server.key" -out "$scratch/server.csr"
    cat > "$scratch/extensions" <<EXTENSIONS
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=$names
EXTENSIONS
    openssl x509 -req -in "$scratch/server.csr" -CA "$scratch/ca.crt" -CAkey "$scratch/ca.key" \
        -days 365 -sha256 -extfile "$scratch/extensions" -out "$scratch/server.crt"
    rm -- "$scratch/ca.key" "$scratch/server.csr" "$scratch/extensions"
    mv -- "$scratch" "$target"
)

issue "$browser" "shiftbox.$suffix" "DNS:shiftbox.$suffix,DNS:*.ide.$suffix,DNS:*.preview.$suffix,DNS:*.repair.$suffix"
issue "$postgres" "postgres.$suffix" "DNS:postgres.$suffix"
for entry in "$browser:shiftbox.$suffix" "$postgres:postgres.$suffix"; do
    identity=${entry%:*}
    hostname=${entry##*:}
    openssl verify -CAfile "$identity/ca.crt" -purpose sslserver -verify_hostname "$hostname" "$identity/server.crt" >/dev/null
    test "$(openssl pkey -in "$identity/server.key" -pubout)" = "$(openssl x509 -in "$identity/server.crt" -pubkey -noout)"
done

install -d -m 0755 "$database_trust"
public_ca=$(mktemp "$database_trust/.ca.XXXXXXXX")
trap 'rm -f -- "$public_ca"' EXIT
cat "$postgres/ca.crt" > "$public_ca"
chmod 0444 "$public_ca"
mv -f -- "$public_ca" "$database_trust/ca.crt"
chown -R 10001:10001 "$browser"
# The Postgres start script runs as root and stages the database leaf/key with
# the postgres container's own user. No other task mounts this directory.
chown -R 0:0 "$postgres"

# The public CA is the only certificate material sent to AWS. A stable token
# makes retries idempotent and never replaces the existing client trust anchor.
version=$(openssl dgst -sha256 -r "$browser/ca.crt" | cut -d ' ' -f 1)
AWS_PAGER='' aws --region "$region" secretsmanager put-secret-value --secret-id "$trust_secret" \
    --client-request-token "$version" --secret-string "file://$browser/ca.crt" >/dev/null
