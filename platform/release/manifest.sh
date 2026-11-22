#!/usr/bin/env bash

# The installer embeds these functions before its entry point.
manifest_fail() {
  printf 'ShiftBox release: %s\n' "$*" >&2
  return 1
}

# jq expands its own variables when it evaluates the filter.
# shellcheck disable=SC2016
manifest_filter='
  def supported_regions: ["us-east-1", "us-east-2", "us-west-1", "us-west-2"];
  def shape($fields): type == "object" and keys == ($fields | sort);
  def matches($pattern): type == "string" and test($pattern + "\\z");
  def sha256: matches("^[0-9a-f]{64}");
  def commit: matches("^[0-9a-f]{40}");
  def ami: matches("^ami-[0-9a-f]{17}");
  def semver: "v(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)";
  def candidate: matches("^" + semver + "-rc[.][1-9][0-9]*");
  def bucket: matches("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]");
  def object_version: . != "null" and matches("^[!-~]{1,1024}");
  def location_url($region):
    "https://\(.bucket).s3.\($region).amazonaws.com/\(.key)?versionId=\(.versionId | @uri)";
  def console_url($region; $url):
    "https://\($region).console.aws.amazon.com/cloudformation/home?region=\($region)#/stacks/create/review?templateURL=\($url | @uri)";
  def asset($name):
    shape(["name", "sha256"]) and .name == $name and (.sha256 | sha256);
  def workspace($release; $region):
    shape(["amiId", "rootDeviceName", "rootSnapshotId", "sourceAmiId", "sourceRegion",
        "sourceCommit", "guestRelease", "architecture", "minimumDiskGiB", "catalogPath", "catalogSha256"]) and
    (.amiId | ami) and (.rootDeviceName | matches("^/dev/[a-z0-9]+")) and
    (.rootSnapshotId | matches("^snap-[0-9a-f]{17}")) and (.sourceAmiId | ami) and
    (.sourceRegion | type == "string" and matches("^[a-z]{2}-[a-z]+-[0-9]+")) and
    .sourceCommit == $release.sourceCommit and .guestRelease == $release.candidateTag and
    .architecture == "amd64" and
    .minimumDiskGiB == 32 and
    .catalogPath == "catalog/\($region).json" and (.catalogSha256 | sha256);
  def location($release; $region):
    shape(["bucket", "key", "versionId", "url", "quickCreateUrl", "sha256"]) and
    (.bucket | bucket) and (.versionId | object_version) and
    .key == "public/templates/\($release.candidateTag)/\($release.template.sha256)/\($release.template.name)" and
    .sha256 == $release.template.sha256 and .url == location_url($region) and
    (.url | length <= 1024) and .quickCreateUrl == console_url($region; .url);
  def regional($release):
    shape(supported_regions) and all(to_entries[];
      .key as $region | .value |
      shape(["hostAmiId", "workspaceImage", "templateLocation"]) and
      (.hostAmiId | ami) and (.workspaceImage | workspace($release; $region)) and
      (.templateLocation | location($release; $region)));
  def distribution:
    . as $release |
    shape(["schemaVersion", "candidateTag", "sourceCommit", "runtimeCompatibility", "publisherAccountId",
      "controller", "postgresImage", "template", "installer", "supportAssets", "regions"]) and
    .schemaVersion == 1 and (.candidateTag | candidate) and (.sourceCommit | commit) and
    .runtimeCompatibility == .candidateTag and (.publisherAccountId | matches("^[0-9]{12}")) and
    (.controller |
      shape(["repository", "digest", "platform"]) and .repository == "ghcr.io/jflatten/shiftbox" and
      .platform == "linux/amd64" and (.digest | matches("^sha256:[0-9a-f]{64}"))) and
    (.postgresImage | matches("^docker[.]io/library/postgres:17-(bookworm|bullseye|trixie)@sha256:[0-9a-f]{64}")) and
    (.template | asset("shiftbox-aws-cloudformation.yaml")) and (.installer | asset("install.sh")) and
    (.supportAssets | asset("shiftbox-aws-support.tar.gz")) and (.regions | regional($release));
'

manifest_validate() {
  if ! jq -es --arg tag "$2" --arg source "$3" --arg publisher "$4" "$manifest_filter"'
    length == 1 and (.[0] |
      distribution and .sourceCommit == $source and .publisherAccountId == $publisher and
      ($tag == .candidateTag or $tag == (.candidateTag | split("-rc.")[0])))
  ' <"$1" >/dev/null; then
    manifest_fail "manifest does not match the approved release, source, publisher, or regional assets"
  fi
}

manifest_quick_create() {
  jq -ner --arg region "$1" --arg url "$2" "$manifest_filter"'
    select(supported_regions | index($region)) |
    select($url | length <= 1024) |
    $url | capture("^https://(?<bucket>[a-z0-9-]+)[.]s3[.]" + $region +
      "[.]amazonaws[.]com/public/templates/(?<tag>" + semver +
      "-rc[.][1-9][0-9]*)/[0-9a-f]{64}/shiftbox-aws-cloudformation[.]yaml[?]versionId=(?<version>[A-Za-z0-9_.~-]|%[A-F0-9]{2})+\\z") |
    select(.bucket | bucket) |
    select(($url | split("?versionId=")[1]) != "null") |
    console_url($region; $url)
  '
}

manifest_validate_support() (
  local archive=$1 scratch
  scratch=$(mktemp -d) || return 1
  trap 'rm -rf "$scratch"' EXIT
  if ! tar -tzf - <"$archive" >"$scratch/paths" || ! tar -tvzf - <"$archive" >"$scratch/entries"; then
    manifest_fail "support archive cannot be read"
    return 1
  fi

  # Only the fixed packaging layout can be extracted, with no links or duplicate paths.
  if ! awk '
    NR == FNR { paths[NR] = $0; next }
    {
      path = paths[FNR]
      type = substr($0, 1, 1)
      if ((path == "catalog/" || path == "docs/") && type == "d") {
        sub(/\/$/, "", path)
      } else if (!(type == "-" && path ~ /^(catalog\/(us-east-1|us-east-2|us-west-1|us-west-2)\.json|docs\/[a-z0-9][a-z0-9-]*\.md|stack-policy\.json|setup\.sh)$/)) {
        bad = 1
      }
      if (seen[path]++) bad = 1
      if (path ~ /^docs\/.+\.md$/) docs++
    }
    END {
      if (NR != 2 * FNR || bad || !docs || !seen["setup.sh"] || !seen["stack-policy.json"] ||
          !seen["catalog/us-east-1.json"] || !seen["catalog/us-east-2.json"] ||
          !seen["catalog/us-west-1.json"] || !seen["catalog/us-west-2.json"]) exit 1
    }
  ' "$scratch/paths" "$scratch/entries"; then
    manifest_fail "support archive has unexpected, missing or unsafe entries"
  fi
)

manifest_sha256() {
  local hash
  hash=$(openssl dgst -sha256 -r <"$1") || return 1
  printf '%s\n' "${hash%% *}"
}

manifest_registry_get() {
  local token=$1 url=$2 destination=$3
  # The anonymous pull token stays off the command line. Curl does not forward
  # Authorization to a different host when following an ordinary redirect.
  printf 'header = "Authorization: Bearer %s"\n' "$token" |
    curl --config - --fail --silent --show-error --proto '=https' --proto-redir '=https' \
      --location --max-redirs 2 --connect-timeout 10 --max-time 60 --output "$destination" \
      --header 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
      "$url"
}

manifest_verify_controller() (
  local repository=$1 expected=$2 scratch token image config api
  if [ "$repository" != ghcr.io/jflatten/shiftbox ] || ! [[ "$expected" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    manifest_fail "unexpected public OCI package or digest"
    return 1
  fi
  scratch=$(mktemp -d) || return 1
  trap 'rm -rf -- "$scratch"' EXIT
  curl --fail --silent --show-error --proto '=https' --connect-timeout 10 --max-time 30 \
    --output "$scratch/token.json" 'https://ghcr.io/token?scope=repository%3Ajflatten%2Fshiftbox%3Apull' || return 1
  token=$(jq -er '.token | strings | select(test("^[A-Za-z0-9._=/+~-]{1,4096}\\z"))' <"$scratch/token.json") || return 1
  api=https://ghcr.io/v2/jflatten/shiftbox
  manifest_registry_get "$token" "$api/manifests/$expected" "$scratch/image.json" || return 1
  [ "sha256:$(manifest_sha256 "$scratch/image.json")" = "$expected" ] || return 1
  if jq -e 'has("manifests")' <"$scratch/image.json" >/dev/null; then
    image=$(jq -er '
      [.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64")] |
      select(length == 1) | .[0].digest | strings | select(test("^sha256:[0-9a-f]{64}\\z"))
    ' <"$scratch/image.json") || return 1
    manifest_registry_get "$token" "$api/manifests/$image" "$scratch/image.json" || return 1
    [ "sha256:$(manifest_sha256 "$scratch/image.json")" = "$image" ] || return 1
  fi
  config=$(jq -er 'select(.schemaVersion == 2) | .config.digest |
    strings | select(test("^sha256:[0-9a-f]{64}\\z"))' <"$scratch/image.json") || return 1
  manifest_registry_get "$token" "$api/blobs/$config" "$scratch/config.json" || return 1
  [ "sha256:$(manifest_sha256 "$scratch/config.json")" = "$config" ] || return 1
  jq -e '.os == "linux" and .architecture == "amd64"' <"$scratch/config.json" >/dev/null
)

manifest_verify_assets() (
  local file=$1 directory=$2 kind name expected archive region catalog scratch actual
  if ! jq -es "$manifest_filter"' length == 1 and (.[0] | distribution)' <"$file" >/dev/null; then
    manifest_fail "cannot verify assets without a valid distribution manifest"
    return 1
  fi
  for kind in template installer supportAssets; do
    name=$(jq -er --arg kind "$kind" '.[$kind].name' <"$file") || return 1
    expected=$(jq -er --arg kind "$kind" '.[$kind].sha256' <"$file") || return 1
    actual=$(manifest_sha256 "$directory/$name") || return 1
    if [ "$actual" != "$expected" ]; then
      manifest_fail "checksum mismatch for $name"
      return 1
    fi
  done
  archive="$directory/shiftbox-aws-support.tar.gz"
  manifest_validate_support "$archive" || return 1
  scratch=$(mktemp -d) || return 1
  trap 'rm -rf "$scratch"' EXIT
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    catalog="catalog/$region.json"
    expected=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.catalogSha256' <"$file") || return 1
    tar -xOzf - "$catalog" <"$archive" >"$scratch/catalog.json" || return 1
    actual=$(manifest_sha256 "$scratch/catalog.json") || return 1
    if [ "$actual" != "$expected" ]; then
      manifest_fail "catalog checksum mismatch for $region"
      return 1
    fi
  done
)

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  case "${1:-}:$#" in
    validate:5) manifest_validate "$2" "$3" "$4" "$5" ;;
    verify-assets:3) manifest_verify_assets "$2" "$3" ;;
    verify-controller:3) manifest_verify_controller "$2" "$3" ;;
    quick-create:3) manifest_quick_create "$2" "$3" ;;
    validate-support:2) manifest_validate_support "$2" ;;
    *) manifest_fail "usage: manifest.sh {validate|verify-assets|quick-create|validate-support} arguments" ;;
  esac
fi
