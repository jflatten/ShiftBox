#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../../.." && pwd)
site="$root/docs/site"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
export BUNDLE_GEMFILE="$site/Gemfile"
fixture="$scratch/source"
fixture_site="$fixture/docs/site"
mkdir -p "$fixture_site" "$fixture/branding/png" "$scratch/bin"

cp "$site/build.sh" "$site/_config.yml" "$fixture_site/"
cp -R "$site/_layouts" "$fixture_site/"
cp "$root/branding/png/shiftbox-logo-light-1600.png" "$fixture/branding/png/"

# Build the public guides without exposing neighboring files or branding.
for guide in index install first-workspace contributing private-access troubleshooting upgrade-recovery; do
    cp "$site/$guide.md" "$fixture_site/$guide.md"
done
printf 'PRIVATE_FIXTURE_SENTINEL account record\n' >"$fixture_site/private-evidence.md"
printf 'PRIVATE_FIXTURE_SENTINEL deployment state\n' >"$fixture_site/deploy.tfstate"
printf 'PRIVATE_FIXTURE_SENTINEL branding\n' >"$fixture/branding/secret.txt"

# A Pages build uses the checked-out guides even if an old publisher setting is present.
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export PAGES_TEST_TRACE="$scratch/network-calls"
cat >"$scratch/bin/curl" <<'CURL'
#!/bin/sh
printf '%s\n' "$*" >>"$PAGES_TEST_TRACE"
exit 1
CURL
cp "$scratch/bin/curl" "$scratch/bin/git"
chmod +x "$scratch/bin/"*
PATH="$scratch/bin:$PATH" "$fixture_site/build.sh" "$scratch/rendered" >/dev/null
test ! -s "$PAGES_TEST_TRACE"
if grep -REq 'PRIVATE_FIXTURE_SENTINEL|github\.com/jflatten/ShiftBox/releases|v1\.0\.0-rc\.3' "$scratch/rendered"; then
    exit 1
fi

# Readers can follow the current network and deployment steps through the first workspace.
install="$scratch/rendered/install/index.html"
test -f "$scratch/rendered/first-workspace/index.html"
grep -Fq 'href="/ShiftBox/install/"' "$scratch/rendered/index.html"
grep -Fq '/ShiftBox/assets/shiftbox-logo.png' "$scratch/rendered/index.html"
grep -Fq 'id="prepare-your-vpc-and-subnets"' "$install"
grep -Fq 'id="create-the-cloudformation-stack"' "$install"
grep -Fq 'id="complete-the-protected-browser-setup"' "$install"
grep -Fq 'href="/ShiftBox/private-access/"' "$install"
grep -Fq 'href="/ShiftBox/first-workspace/"' "$install"
grep -Fq 'id="connect-your-github-account"' "$scratch/rendered/first-workspace/index.html"
