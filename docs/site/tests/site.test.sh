#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../../.." && pwd)
site="$root/docs/site"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
export BUNDLE_GEMFILE="$site/Gemfile"
export SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID=111122223333
export PAGES_TEST_FEED="$scratch/feed" PAGES_TEST_COMMIT=0123456789abcdef0123456789abcdef01234567
export PAGES_TEST_TRACE="$scratch/curl-calls"
fixture="$scratch/source"
fixture_site="$fixture/docs/site"
mkdir -p "$fixture_site" "$fixture/platform/release" "$fixture/branding/png" "$scratch/bin" "$PAGES_TEST_FEED"

cp "$site/build.sh" "$site/_config.yml" "$fixture_site/"
cp -R "$site/_layouts" "$fixture_site/"
cp "$root/platform/release/manifest.sh" "$fixture/platform/release/"
cp "$root/branding/png/shiftbox-logo-light-1600.png" "$fixture/branding/png/"

# Build the real public guides without exposing neighboring files or branding.
for guide in index install first-workspace contributing private-access troubleshooting upgrade-recovery; do
    cp "$site/$guide.md" "$fixture_site/$guide.md"
done
printf 'PRIVATE_FIXTURE_SENTINEL account record\n' >"$fixture_site/private-evidence.md"
printf 'PRIVATE_FIXTURE_SENTINEL deployment state\n' >"$fixture_site/deploy.tfstate"
printf 'PRIVATE_FIXTURE_SENTINEL branding\n' >"$fixture/branding/secret.txt"

candidate=v2.0.1-rc.2
stable=v2.0.1
draft=v2.1.0-rc.3
hash=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
catalog_hashes=$(jq -n --arg hash "$hash" '
  reduce ("us-east-1","us-east-2","us-west-1","us-west-2") as $region ({}; .[$region]=$hash)')
jq -n --arg tag "$candidate" --arg commit "$PAGES_TEST_COMMIT" --arg publisher "$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID" \
  --arg template "$hash" --arg installer "$hash" --arg support "$hash" --argjson catalog "$catalog_hashes" \
  -f "$root/platform/release/tests/distribution-fixture.jq" >"$PAGES_TEST_FEED/$candidate.manifest"
cp "$PAGES_TEST_FEED/$candidate.manifest" "$PAGES_TEST_FEED/$stable.manifest"
manifest_digest=$(openssl dgst -sha256 -r <"$PAGES_TEST_FEED/$candidate.manifest")
manifest_digest=${manifest_digest%% *}
release() {
    jq -n --arg tag "$1" --argjson prerelease "$2" --argjson draft "$3" \
      --arg digest "$manifest_digest" --argjson id "$4" '
      "https://github.com/jflatten/ShiftBox/releases" as $releases |
      {id:$id,tag_name:$tag,immutable:true,prerelease:$prerelease,draft:$draft,
        published_at:"2026-05-01T02:03:04Z",html_url:($releases+"/tag/"+$tag),
        body:"PRIVATE_FIXTURE_SENTINEL release content",
        assets:[{name:"distribution-manifest.json",state:"uploaded",digest:("sha256:"+$digest),
                 browser_download_url:($releases+"/download/"+$tag+"/distribution-manifest.json")}] }'
}
release "$stable" false false 101 >"$PAGES_TEST_FEED/latest.json"
release "$candidate" true false 100 >"$scratch/candidate.json"
release "$draft" true true 102 >"$scratch/draft.json"
jq -s '.' "$scratch/draft.json" "$scratch/candidate.json" "$PAGES_TEST_FEED/latest.json" >"$PAGES_TEST_FEED/releases.json"

cat >"$scratch/bin/curl" <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
url=${!#}
printf '%s\n' "$url" >>"$PAGES_TEST_TRACE"
case "$url" in
  https://api.github.com/repos/jflatten/ShiftBox/releases/latest)
    file="$PAGES_TEST_FEED/latest.json"
    ;;
  'https://api.github.com/repos/jflatten/ShiftBox/releases?per_page=100&page=1')
    file="$PAGES_TEST_FEED/releases.json"
    ;;
  https://github.com/jflatten/ShiftBox/releases/download/*/distribution-manifest.json)
    tag=${url%/distribution-manifest.json}
    file="$PAGES_TEST_FEED/${tag##*/}.manifest"
    ;;
  *) printf 'Unexpected Pages fixture HTTP request\n' >&2; exit 20 ;;
esac
[[ -f "$file" ]] || exit 22
cat "$file"
CURL
cat >"$scratch/bin/git" <<'GIT'
#!/bin/sh
set -eu
case "$1:$2:$3" in
    ls-remote:--tags:https://github.com/jflatten/ShiftBox.git)
        printf '%s\t%s\n' "$PAGES_TEST_COMMIT" refs/tags/v2.0.1-rc.2
        printf '%s\t%s\n' 0000000000000000000000000000000000000001 refs/tags/v2.0.1
        printf '%s\t%s\n' "$PAGES_TEST_COMMIT" 'refs/tags/v2.0.1^{}'
        ;;
    *) printf 'Unexpected Pages fixture Git request\n' >&2; exit 20 ;;
esac
GIT
chmod +x "$scratch/bin/"*
export PATH="$scratch/bin:$PATH"

render() {
    "$fixture_site/build.sh" "$scratch/$1" >/dev/null
}

# Stable selection is separate from the RC list and presents all four exact launch links.
render ready
test -f "$scratch/ready/install/index.html"
grep -Fq '/ShiftBox/install/' "$scratch/ready/index.html"
grep -Fq '/ShiftBox/assets/shiftbox-logo.png' "$scratch/ready/index.html"
grep -Fq 'Latest stable' "$scratch/ready/index.html"
grep -Fq "https://github.com/jflatten/ShiftBox/releases/tag/$candidate" "$scratch/ready/index.html"
for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    link=$(jq -r --arg region "$region" '.regions[$region].templateLocation.quickCreateUrl' "$PAGES_TEST_FEED/$candidate.manifest")
    grep -Fq "${link//&/&amp;}" "$scratch/ready/index.html"
done
test ! -e "$scratch/ready/private-evidence.md"
test ! -e "$scratch/ready/deploy.tfstate"
test ! -e "$scratch/ready/branding/secret.txt"
if grep -RFq 'PRIVATE_FIXTURE_SENTINEL' "$scratch/ready"; then exit 1; fi
if grep -Fq "$draft" "$scratch/ready/index.html"; then exit 1; fi

# Operators can move from prerequisites to regional launch, private access and the IDE guide.
test -f "$scratch/ready/first-workspace/index.html"
grep -Fq 'href="#releases-heading"' "$scratch/ready/install/index.html"
grep -Fq 'href="/ShiftBox/private-access/#connect-with-tailscale"' "$scratch/ready/install/index.html"
grep -Fq 'href="/ShiftBox/first-workspace/"' "$scratch/ready/install/index.html"
grep -Fq 'id="connect-with-tailscale"' "$scratch/ready/private-access/index.html"
grep -Fq 'id="connect-your-github-account"' "$scratch/ready/first-workspace/index.html"

# The install page labels current-source networking separately from the RC3 procedure.
grep -Fq 'id="choose-your-deployment"' "$scratch/ready/install/index.html"
grep -Fq 'href="https://github.com/jflatten/ShiftBox/tree/main/platform/cloudformation/aws"' "$scratch/ready/install/index.html"
grep -Fq 'id="before-you-begin-with-rc3"' "$scratch/ready/install/index.html"

# Before stable, explicit RC links remain available without claiming a default.
mv "$PAGES_TEST_FEED/latest.json" "$scratch/stable.json"
render pre-stable
grep -Fq '<details open>' "$scratch/pre-stable/index.html"
grep -Fq "https://github.com/jflatten/ShiftBox/releases/tag/$candidate" "$scratch/pre-stable/index.html"
if grep -Fq 'Latest stable' "$scratch/pre-stable/index.html"; then exit 1; fi
mv "$scratch/stable.json" "$PAGES_TEST_FEED/latest.json"

# When the manifests are unavailable, use only canonical release URLs.
mv "$PAGES_TEST_FEED/$stable.manifest" "$scratch/stable.manifest"
printf '{"stale":true}\n' >"$PAGES_TEST_FEED/$candidate.manifest"
render fallback
grep -Fq 'https://github.com/jflatten/ShiftBox/releases' "$scratch/fallback/index.html"
if grep -Fq 'console.aws.amazon.com' "$scratch/fallback/index.html"; then exit 1; fi

# A checkout without an approved publisher can build docs without network reads.
: >"$PAGES_TEST_TRACE"
SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID='' render offline
test ! -s "$PAGES_TEST_TRACE"
grep -Fq 'https://github.com/jflatten/ShiftBox/releases' "$scratch/offline/index.html"
