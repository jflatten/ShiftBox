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
mkdir -p "$fixture_site/assets" "$fixture/branding/png" "$scratch/bin"

cp "$site/build.sh" "$site/_config.yml" "$fixture_site/"
cp -R "$site/_layouts" "$fixture_site/"
cp "$root/branding/png/shiftbox-logo-light-1600.png" "$fixture/branding/png/"

# Build the public guides without exposing neighboring files or branding.
for guide in index install setup-helper first-workspace contributing private-access troubleshooting upgrade-recovery; do
    cp "$site/$guide.md" "$fixture_site/$guide.md"
done
for screenshot in setup-administrator.png setup-activate.png; do
    cp "$site/assets/$screenshot" "$fixture_site/assets/"
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
if grep -REq 'PRIVATE_FIXTURE_SENTINEL|v1\.0\.0-rc\.3|/releases/(tag|download|latest)' "$scratch/rendered"; then
    exit 1
fi

# Readers start with public downloads and can deploy through the AWS Console.
install="$scratch/rendered/install/index.html"
grep -Fq 'href="https://github.com/jflatten/ShiftBox/releases"' "$install"
grep -Fq 'id="get-the-installation-files"' "$install"
grep -Fq 'id="retrieve-the-certificate-and-admin-claim"' "$install"
grep -Fq 'href="/ShiftBox/private-access/#trust-the-browser-certificate"' "$install"
for screenshot in setup-administrator.png setup-activate.png; do
    test -s "$scratch/rendered/assets/$screenshot"
    grep -Fq "src=\"/ShiftBox/assets/$screenshot\"" "$install"
done
for output in BrowserCaSecretArn SetupClaimSecretArn ManagementOrigin; do
    grep -Fq "$output" "$install"
done
for guide in index.html install/index.html private-access/index.html first-workspace/index.html troubleshooting/index.html upgrade-recovery/index.html; do
    if grep -Eq '/(blob|tree)/main|<code[^>]*>main</code>|git (clone|switch|checkout)|one-pass development|SetupOperatorPolicyArn|PostgresCaPem|dns-zones|setup\.sh' "$scratch/rendered/$guide"; then
        exit 1
    fi
done

# Advanced templates keep the terminal setup guide on a separate page.
grep -Fq 'href="/ShiftBox/install/setup-helper/"' "$install"
grep -Fq 'sh ./setup.sh STACK_ID AWS_REGION AWS_ACCOUNT_ID' "$scratch/rendered/install/setup-helper/index.html"

# Readers can follow the network and deployment steps through the first workspace.
test -f "$scratch/rendered/first-workspace/index.html"
grep -Fq 'href="/ShiftBox/install/"' "$scratch/rendered/index.html"
grep -Fq '/ShiftBox/assets/shiftbox-logo.png' "$scratch/rendered/index.html"
grep -Fq 'id="prepare-your-vpc-and-subnets"' "$install"
grep -Fq 'id="create-the-cloudformation-stack"' "$install"
grep -Fq 'id="finish-setup-in-shiftbox"' "$install"
grep -Fq 'href="/ShiftBox/private-access/"' "$install"
grep -Fq 'href="/ShiftBox/first-workspace/"' "$install"
grep -Fq 'id="connect-your-github-account"' "$scratch/rendered/first-workspace/index.html"
