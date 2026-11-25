#!/bin/sh
set -eu
umask 077

# The private key stays in the task filesystem. Only database data uses EBS.
tls=/tmp/shiftbox-postgres-tls
install -d -m 0700 -o postgres -g postgres "$tls"
if [ -d /run/shiftbox/postgres-tls ]; then
    cat /run/shiftbox/postgres-tls/server.crt > "$tls/server.crt"
    cat /run/shiftbox/postgres-tls/server.key > "$tls/server.key"
else
    printf '%s\n' "${POSTGRES_TLS_CERT:?missing database certificate}" > "$tls/server.crt"
    printf '%s\n' "${POSTGRES_TLS_KEY:?missing database private key}" > "$tls/server.key"
fi
chown postgres:postgres "$tls/server.crt" "$tls/server.key"
unset POSTGRES_TLS_CERT POSTGRES_TLS_KEY

cat > "$tls/pg_hba.conf" <<'HBA'
local all all trust
hostssl shiftbox shiftbox_app,shiftbox_dex all scram-sha-256
HBA
chown postgres:postgres "$tls/pg_hba.conf"

# The official entrypoint runs this script only on a new PGDATA directory.
cat > /docker-entrypoint-initdb.d/01-shiftbox.sql <<'SQL'
\getenv app_password SHIFTBOX_APP_PASSWORD
\getenv dex_password SHIFTBOX_DEX_PASSWORD
CREATE ROLE shiftbox_app LOGIN PASSWORD :'app_password';
CREATE ROLE shiftbox_dex LOGIN PASSWORD :'dex_password';
CREATE SCHEMA shiftbox AUTHORIZATION shiftbox_app;
CREATE SCHEMA dex AUTHORIZATION shiftbox_dex;
ALTER ROLE shiftbox_app IN DATABASE shiftbox SET search_path TO shiftbox;
ALTER ROLE shiftbox_dex IN DATABASE shiftbox SET search_path TO dex;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON DATABASE shiftbox FROM PUBLIC;
GRANT CONNECT ON DATABASE shiftbox TO shiftbox_app, shiftbox_dex;
SQL
chmod 0644 /docker-entrypoint-initdb.d/01-shiftbox.sql

exec docker-entrypoint.sh postgres -c ssl=on -c "ssl_cert_file=$tls/server.crt" \
    -c "ssl_key_file=$tls/server.key" -c "hba_file=$tls/pg_hba.conf"
