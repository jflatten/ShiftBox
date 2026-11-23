#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/manifest.sh"

repository=jflatten/ShiftBox
stable_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
candidate_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'
assets=(distribution-manifest.json install.sh shiftbox-aws-cloudformation.yaml shiftbox-aws-support.tar.gz)

fail() {
  printf 'Release publication: %s\n' "$*" >&2
  return 1
}

channel() {
  local tag=$1
  [ "${#tag}" -le 96 ] || { fail 'version tag exceeds 96 characters'; return 1; }
  if [[ "$tag" =~ $candidate_pattern ]]; then
    printf 'rc\n'
  elif [[ "$tag" =~ $stable_pattern ]]; then
    printf 'stable\n'
  else
    fail 'expected a canonical RC or stable version tag'
  fi
}

check_event() {
  local actor=${GITHUB_ACTOR:-} rerunner=${GITHUB_TRIGGERING_ACTOR:-}
  local list=${SHIFTBOX_RELEASE_MAINTAINERS:-} ref=${GITHUB_REF:-}
  [ "${GITHUB_REPOSITORY:-}" = "$repository" ] && [ "${GITHUB_EVENT_NAME:-}" = push ] &&
    [ "${GITHUB_REF_TYPE:-}" = tag ] && [ "${GITHUB_REF_PROTECTED:-}" = true ] &&
    [[ "$ref" == refs/tags/* ]] || { fail 'expected a protected tag push in the release repository'; return 1; }
  [[ "$list" =~ ^[a-zA-Z0-9-]+(,[a-zA-Z0-9-]+)*$ ]] &&
    [[ "$actor" =~ ^[a-zA-Z0-9-]+$ ]] && [[ "$rerunner" =~ ^[a-zA-Z0-9-]+$ ]] &&
    [[ ",$list," == *",$actor,"* ]] && [[ ",$list," == *",$rerunner,"* ]] ||
    { fail 'the tag creator and workflow rerun actor must be approved maintainers'; return 1; }
  channel "${ref#refs/tags/}"
}

check_source() {
  local tag=$1 event_commit=$2 requested source
  channel "$tag" >/dev/null || return 1
  [[ "$event_commit" =~ ^[0-9a-f]{40}$ ]] || { fail 'expected a full protected-event commit ID'; return 1; }
  requested=$(git rev-parse --verify "$event_commit^{commit}") || return 1
  source=$(git rev-parse --verify "refs/tags/$tag^{commit}") || return 1
  if [ "$source" != "$requested" ] || ! git merge-base --is-ancestor "$source" refs/remotes/origin/main; then
    fail 'the fresh tag does not match the protected event and reviewed main'
    return 1
  fi
  printf '%s\n' "$source"
}

check_stable_latest() {
  local requested=$1 previous=$2 part
  local -a target_version previous_version
  [[ "$requested" =~ $stable_pattern && "$previous" =~ $stable_pattern ]] ||
    { fail 'latest selection requires stable version tags'; return 1; }
  IFS=. read -r -a target_version <<<"${requested#v}"
  IFS=. read -r -a previous_version <<<"${previous#v}"
  # Compare lengths first so large version components do not overflow shell arithmetic.
  for part in 0 1 2; do
    if [ "${#target_version[$part]}" -gt "${#previous_version[$part]}" ]; then
      return 0
    elif [ "${#target_version[$part]}" -lt "${#previous_version[$part]}" ]; then
      break
    elif [[ "${target_version[$part]}" > "${previous_version[$part]}" ]]; then
      return 0
    elif [[ "${target_version[$part]}" < "${previous_version[$part]}" ]]; then
      break
    fi
  done
  [ "$requested" = "$previous" ] || fail 'refusing to move the default stable release backwards'
}

validate_assets() {
  local tag=$1 source=$2 directory=$3
  [ -n "${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}" ] ||
    { fail 'the approved publisher account is required'; return 1; }
  manifest_validate "$directory/distribution-manifest.json" "$tag" "$source" "$SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID" &&
    manifest_verify_assets "$directory/distribution-manifest.json" "$directory"
}

marker() {
  local directory=$1 source=$2 checksum
  checksum=$(manifest_sha256 "$directory/distribution-manifest.json") || return 1
  printf '<!-- shiftbox-release manifest-sha256=%s source=%s -->\n' "$checksum" "$source"
}

verify_draft() {
  local tag=$1 source=$2 directory=$3 release=$4 phase identity expected_manifest expected_assets
  phase=$(channel "$tag") || return 1
  validate_assets "$tag" "$source" "$directory" || return 1
  identity=$(marker "$directory" "$source") || return 1
  expected_manifest=$(manifest_sha256 "$directory/distribution-manifest.json") || return 1
  expected_assets=$(jq -c --arg manifest "$expected_manifest" '
    [.installer, .template, .supportAssets, {name:"distribution-manifest.json",sha256:$manifest}]
    | map({key:.name,value:("sha256:"+.sha256)}) | from_entries
  ' "$directory/distribution-manifest.json") || return 1
  if ! jq -er --arg tag "$tag" --arg phase "$phase" --arg marker "$identity" --argjson expected "$expected_assets" '
    select((.id | type == "number" and . > 0) and .tag_name == $tag and .prerelease == ($phase == "rc"))
    | select((.body | split("\n") | map(select(startswith("<!-- shiftbox-release ")))) == [$marker])
    | select((.assets | type == "array") and
        ((.assets | unique_by(.name) | length) == (.assets | length)) and
        all(.assets[]; .name as $name | .state == "uploaded" and
          ($expected | has($name)) and .digest == $expected[$name]))
    | if .draft == true and .immutable != true then "draft"
      elif .draft == false and .immutable == true and ((.assets | length) == ($expected | length)) then "published"
      else empty end
  ' "$release"; then
    fail 'release draft or published assets conflict with the verified payload'
  fi
}

verify_settings() {
  local settings_token=${SHIFTBOX_RELEASE_SETTINGS_TOKEN:-} rules ids id detail
  [ -n "$settings_token" ] || { fail 'the repository policy read credential is required'; return 1; }
  GH_TOKEN="$settings_token" gh api "repos/$repository/immutable-releases" |
    jq -e '.enabled == true' >/dev/null || { fail 'immutable releases are not verified'; return 1; }
  rules=$(GH_TOKEN="$settings_token" gh api --paginate --slurp "repos/$repository/rulesets?per_page=100") || return 1
  ids=$(jq -r '.[][] | select(.target == "tag" and .enforcement == "active") | .id' <<<"$rules") || return 1
  rules='[]'
  for id in $ids; do
    [[ "$id" =~ ^[0-9]+$ ]] || { fail 'a repository ruleset returned an invalid identifier'; return 1; }
    detail=$(GH_TOKEN="$settings_token" gh api "repos/$repository/rulesets/$id") || return 1
    rules=$(jq --argjson detail "$detail" '. + [$detail]' <<<"$rules") || return 1
  done
  # A maintainer confirms bypass permissions before configuring release access.
  # The API can omit bypass actors for a read-only credential.
  jq -e '
    [ .[]
      | select(.target == "tag" and .enforcement == "active")
      | .conditions.ref_name as $scope
      | select(($scope.exclude == []) and any($scope.include[]; . == "refs/tags/v*" or . == "~ALL"))
      | .rules[].type ] as $rules
    | all("creation", "update", "deletion"; . as $type | $rules | index($type) != null)
  ' <<<"$rules" >/dev/null || fail 'version tags lack verified creation, update and deletion rules'
}

read_releases() {
  gh api --paginate --slurp "repos/$repository/releases?per_page=100" |
    jq -e 'if type == "array" and all(.[]; type == "array") then (add // []) else error("invalid release pages") end'
}

select_release() {
  local tag=$1
  jq -e --arg tag "$tag" '
    map(select(.tag_name == $tag)) |
    if length == 1 then .[0] elif length == 0 then null else error("more than one release matches tag") end
  '
}

check_context() {
  local tag=$1 source=$2 verified
  check_event >/dev/null || return 1
  [ "$GITHUB_REF" = "refs/tags/$tag" ] || { fail 'release tag does not match the workflow ref'; return 1; }
  verified=$(check_source "$tag" "${GITHUB_SHA:-}") || return 1
  [ "$verified" = "$source" ] || fail 'release source does not match the workflow push'
}

assert_complete_draft() {
  local tag=$1 source=$2 directory=$3 release=$4
  verify_draft "$tag" "$source" "$directory" "$release" >/dev/null &&
    jq -e --argjson count "${#assets[@]}" '.assets | length == $count' "$release" >/dev/null
}

prepare_draft() (
  local tag=$1 source=$2 directory=$3 notes=$4 phase identity current status asset
  check_context "$tag" "$source" || exit 1
  validate_assets "$tag" "$source" "$directory" || exit 1
  phase=$(channel "$tag") || exit 1
  identity=$(marker "$directory" "$source") || exit 1
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  read_releases >"$scratch/releases.json" || exit 1
  if ! select_release "$tag" <"$scratch/releases.json" >"$scratch/current.json"; then
    [ "$(cat "$scratch/current.json")" = null ] || exit 1
    jq -n --arg tag "$tag" --arg source "$source" --arg marker "$identity" --arg phase "$phase" --rawfile notes "$notes" '
      {tag_name:$tag,target_commitish:$source,name:("ShiftBox "+$tag),draft:true,prerelease:($phase == "rc"),
       body:($notes+"\n\n"+$marker+"\n")}
    ' | gh api --method POST "repos/$repository/releases" --input - >"$scratch/current.json" || exit 1
  elif reservation=$(build_reservation "$source") && jq -e --arg body "$reservation" '
    .draft == true and .prerelease == true and (.assets | length) == 0 and .body == $body
  ' "$scratch/current.json" >/dev/null; then
    local id
    id=$(jq -er '.id' "$scratch/current.json") || exit 1
    # A draft notes update can detach the existing tag unless it is sent explicitly.
    jq -n --arg tag "$tag" --arg source "$source" --arg marker "$identity" --rawfile notes "$notes" '
      {tag_name:$tag,target_commitish:$source,draft:true,prerelease:true,body:($notes+"\n\n"+$marker+"\n")}
    ' |
      gh api --method PATCH "repos/$repository/releases/$id" --input - >"$scratch/current.json" || exit 1
  fi
  current="$scratch/current.json"
  status=$(verify_draft "$tag" "$source" "$directory" "$current") || exit 1
  [ "$status" != published ] || exit 0
  for asset in "${assets[@]}"; do
    if ! jq -e --arg name "$asset" 'any(.assets[]; .name == $name)' "$current" >/dev/null; then
      gh release upload "$tag" "$directory/$asset" --repo "$repository" || exit 1
    fi
  done
  read_releases | select_release "$tag" >"$current" || exit 1
  assert_complete_draft "$tag" "$source" "$directory" "$current" ||
    fail 'uploaded draft assets did not match the approved payload'
)

build_reservation() {
  local source=$1
  [[ "${GITHUB_RUN_ID:-}" =~ ^[0-9]+$ ]] || return 1
  printf '<!-- shiftbox-candidate source=%s run=%s -->\n' "$source" "$GITHUB_RUN_ID"
}

reserve_candidate() {
  local tag=$1 source=$2 mode=${3:-new} current reservation
  check_context "$tag" "$source" || return 1
  [ "$(channel "$tag")" = rc ] || { fail 'a build reservation requires an RC tag'; return 1; }
  reservation=$(build_reservation "$source") || return 1
  current=$(read_releases | jq -cer --arg tag "$tag" 'map(select(.tag_name == $tag))') || return 1
  if [ "$mode" = resume ]; then
    jq -e --arg body "$reservation" '
      length == 1 and all(.[];
        .draft == true and .prerelease == true and .immutable != true and .assets == [] and .body == $body)
    ' <<<"$current" >/dev/null || fail 'the frozen handoff has no matching same-run build reservation'
    return
  fi
  if [ "$current" != '[]' ]; then
    fail 'a candidate already exists and this run has no reusable staged payload; inspect the reservation before proceeding'
    return 1
  fi
  # Persist this before building, so a lost response or failed runner cannot rebuild an existing candidate.
  jq -n --arg tag "$tag" --arg source "$source" --arg body "$reservation" '
    {tag_name:$tag,target_commitish:$source,name:("ShiftBox "+$tag),draft:true,prerelease:true,body:$body}
  ' | gh api --method POST "repos/$repository/releases" --input - >/dev/null
}

finalize() (
  local tag=$1 source=$2 directory=$3 phase status prior updated_id latest
  check_context "$tag" "$source" || exit 1
  phase=$(channel "$tag") || exit 1
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  read_releases >"$scratch/releases.json" || exit 1
  select_release "$tag" <"$scratch/releases.json" >"$scratch/current.json" || exit 1
  status=$(verify_draft "$tag" "$source" "$directory" "$scratch/current.json") || exit 1
  [ "$status" != published ] || exit 0
  assert_complete_draft "$tag" "$source" "$directory" "$scratch/current.json" ||
    { fail 'an incomplete draft cannot become public'; exit 1; }
  latest=false
  if [ "$phase" = stable ]; then
    prior=$(jq -r '.[] | select(.draft == false and .prerelease == false) | .tag_name' "$scratch/releases.json") || exit 1
    while IFS= read -r version; do
      if [[ "$version" =~ $stable_pattern ]]; then
        check_stable_latest "$tag" "$version" || exit 1
      fi
    done <<<"$prior"
    latest=true
  fi
  updated_id=$(jq -er '.id' "$scratch/current.json") || exit 1
  gh api --method PATCH "repos/$repository/releases/$updated_id" -F draft=false -f "make_latest=$latest" >"$scratch/published.json" || exit 1
  status=$(verify_draft "$tag" "$source" "$directory" "$scratch/published.json") || exit 1
  [ "$status" = published ] || fail 'the published release was not immutable'
)

case "${1-}:$#" in
  check-event:1) check_event ;;
  check-source:3) check_source "$2" "$3" ;;
  check-stable-latest:3) check_stable_latest "$2" "$3" ;;
  verify-settings:1) verify_settings ;;
  verify-draft:5) verify_draft "$2" "$3" "$4" "$5" ;;
  reserve-candidate:3) reserve_candidate "$2" "$3" ;;
  resume-candidate:3) reserve_candidate "$2" "$3" resume ;;
  prepare-draft:5) prepare_draft "$2" "$3" "$4" "$5" ;;
  finalize:4) finalize "$2" "$3" "$4" ;;
  *) fail 'usage: publish.sh {check-event|check-source|check-stable-latest|verify-settings|verify-draft|reserve-candidate|resume-candidate|prepare-draft|finalize}' ;;
esac
