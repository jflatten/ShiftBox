#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
SH_TEMPLATE_TEST=$(mktemp -d)
export SH_TEMPLATE_TEST
trap 'rm -rf "$SH_TEMPLATE_TEST"' EXIT
mkdir "$SH_TEMPLATE_TEST/bin"
printf 'Resources: {}\n' >"$SH_TEMPLATE_TEST/template.yaml"
cat >"$SH_TEMPLATE_TEST/buckets.json" <<'JSON'
{"us-east-1":"shiftbox-fixture-us-east-1","us-east-2":"shiftbox-fixture-us-east-2","us-west-1":"shiftbox-fixture-us-west-1","us-west-2":"shiftbox-fixture-us-west-2"}
JSON
cat >"$SH_TEMPLATE_TEST/bin/aws" <<'AWS'
#!/usr/bin/env bash
set -eu
region=$2
service=$3
action=$4
shift 4
case "$service:$action" in
    sts:get-caller-identity) printf '%s\n' "${TEST_ACCOUNT:-111122223333}" ;;
    s3api:get-bucket-location)
        if [ "$region" = us-east-1 ]; then printf '{"LocationConstraint":null}\n'
        else printf '{"LocationConstraint":"%s"}\n' "$region"; fi ;;
    s3api:get-bucket-versioning) printf '{"Status":"Enabled"}\n' ;;
    s3api:list-object-versions)
        if [ -e "$SH_TEMPLATE_TEST/$region" ]; then
            while [ "$1" != --prefix ]; do shift; done
            jq -n --arg key "$2" '{Versions:[{Key:$key,VersionId:"candidate+version/one=",IsLatest:true}]}'
        else
            printf '{}\n'
        fi ;;
    s3api:put-object)
        test "$region" != "${TEST_UNAVAILABLE_REGION:-}"
        printf '%s\n' "$region" >>"$SH_TEMPLATE_TEST/writes"
        : >"$SH_TEMPLATE_TEST/$region"
        printf '{"VersionId":"candidate+version/one="}\n' ;;
    *) printf 'Unexpected AWS operation %s:%s\n' "$service" "$action" >&2; exit 97 ;;
esac
AWS
cat >"$SH_TEMPLATE_TEST/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -eu
while [ "$1" != --output ]; do shift; done
out=$2 url=$3
case "$url" in
    *'.amazonaws.com/public/templates/'*'?versionId=candidate%2Bversion%2Fone%3D')
        if [[ "$url" == *".s3.${TEST_DRIFT_REGION:-never}.amazonaws.com/"* ]]; then
            printf 'a different template\n' >"$out"
        else
            cp "$SH_TEMPLATE_TEST/template.yaml" "$out"
        fi ;;
    *) exit 97 ;;
esac
CURL
chmod +x "$SH_TEMPLATE_TEST/bin/aws" "$SH_TEMPLATE_TEST/bin/curl"
export PATH="$SH_TEMPLATE_TEST/bin:$PATH"

stage() {
    "$here/templates.sh" stage 111122223333 v1.4.2-rc.1 "$SH_TEMPLATE_TEST/template.yaml" "$SH_TEMPLATE_TEST/buckets.json"
}

# A foreign publisher fails before touching any bucket.
if TEST_ACCOUNT=444455556666 stage >"$SH_TEMPLATE_TEST/rejected" 2>&1; then exit 1; fi
test ! -e "$SH_TEMPLATE_TEST/writes"

# Failed publication does not return partial regional locations.
if TEST_UNAVAILABLE_REGION=us-west-1 stage >"$SH_TEMPLATE_TEST/partial"; then exit 1; fi
test ! -s "$SH_TEMPLATE_TEST/partial"
test "$(wc -l <"$SH_TEMPLATE_TEST/writes" | tr -d ' ')" = 2

# An identical rerun resumes the missing uploads without replacing public objects.
stage >"$SH_TEMPLATE_TEST/locations.json"
jq -e 'length == 4 and all(.[]; .versionId == "candidate+version/one=")' <"$SH_TEMPLATE_TEST/locations.json" >/dev/null
test "$(wc -l <"$SH_TEMPLATE_TEST/writes" | tr -d ' ')" = 4
stage >"$SH_TEMPLATE_TEST/retry.json"
cmp "$SH_TEMPLATE_TEST/locations.json" "$SH_TEMPLATE_TEST/retry.json"
test "$(wc -l <"$SH_TEMPLATE_TEST/writes" | tr -d ' ')" = 4

# A conflicting hosted version fails closed without overwriting the object.
if TEST_DRIFT_REGION=us-east-2 stage >"$SH_TEMPLATE_TEST/conflict" 2>"$SH_TEMPLATE_TEST/conflict-error"; then exit 1; fi
test ! -s "$SH_TEMPLATE_TEST/conflict"
test "$(wc -l <"$SH_TEMPLATE_TEST/writes" | tr -d ' ')" = 4
printf 'Regional template staging fixtures passed.\n'
