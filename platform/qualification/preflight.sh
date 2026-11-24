#!/usr/bin/env bash
# jq takes literal variables from --arg.
# shellcheck disable=SC2016
set -euo pipefail
umask 077

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
release="$here/../release"
repo=jflatten/ShiftBox
demo=524941137496

fail() {
  printf 'AWS qualification: %s\n' "$*" >&2
  exit 1
}

guard() {
  [[ ${GITHUB_REPOSITORY:-} == "$repo" && ${GITHUB_REF:-} == refs/heads/main ]] ||
    fail 'requires the reviewed main branch of jflatten/ShiftBox'
  [[ ${GITHUB_RUN_ID:-} =~ ^[1-9][0-9]{0,15}$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[1-9][0-9]{0,3}$ ]] ||
    fail 'invalid Actions run identity'
  if [[ $1 != cleanup || ${GITHUB_EVENT_NAME:-} != schedule ]]; then
    [[ ${GITHUB_EVENT_NAME:-} == workflow_dispatch && ${GITHUB_ACTOR:-} == jflatten &&
      ${GITHUB_TRIGGERING_ACTOR:-} == jflatten ]] ||
      fail 'requires an explicit dispatch by the approved maintainer'
  fi
  printf 'sbq-%s-%s\n' "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT"
}

account() {
  local testing=${SHIFTBOX_QUALIFICATION_ACCOUNT_ID:-} publisher=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
  local actual
  [[ $testing =~ ^[0-9]{12}$ && $publisher =~ ^[0-9]{12}$ &&
    $testing != "$demo" && $publisher != "$demo" && $testing != "$publisher" ]] ||
    fail 'configure the dedicated testing and separate publisher accounts'
  actual=$(aws --region us-east-2 sts get-caller-identity --query Account --output text)
  [[ $actual == "$testing" ]] || fail 'AWS credentials are outside the approved testing account'
}

capacity() {
  local mode=$1 region minimum quota running types used
  [[ $mode == staffed || $mode == automated ]] || fail 'invalid capacity check mode'
  account
  for region in us-east-1 us-east-2 us-west-1 us-west-2; do
    minimum=2
    if [[ $region == us-east-2 && $mode == staffed ]]; then minimum=4; fi
    quota=$(aws --region "$region" service-quotas get-service-quota --service-code ec2 \
      --quota-code L-1216C47A --query Quota.Value --output json)
    running=$(aws --region "$region" ec2 describe-instances \
      --filters Name=instance-state-name,Values=pending,running \
      --query 'Reservations[].Instances[].InstanceType' --output json)
    types=$(jq -er 'select(type == "array" and all(.[]; type == "string" and test("^[a-z0-9-]+\\.[a-z0-9]+$"))) |
      unique | join(",")' <<<"$running") || fail "could not verify EC2 instance inventory in $region"
    used=0
    if [[ -n $types ]]; then
      # Use default vCPUs and count pending instances conservatively. Existing
      # infrastructure consumes headroom without becoming qualification-owned.
      used=$(aws --region "$region" ec2 describe-instance-types --filters "Name=instance-type,Values=$types" --output json |
        jq -er --argjson running "$running" '
          [.InstanceTypes[] | {key:.InstanceType,value:.VCpuInfo.DefaultVCpus}] | from_entries as $sizes |
          [$running[] | $sizes[.] | if type == "number" and . > 0 then . else error("missing vCPU count") end] | add
        ') || fail "could not verify existing EC2 vCPUs in $region"
    fi
    jq -e --argjson minimum "$minimum" --argjson used "$used" \
      'type == "number" and . - $used >= $minimum' <<<"$quota" >/dev/null ||
      fail "$region needs at least $minimum available Standard On-Demand vCPUs after accounting for existing EC2 instances"
  done
}

fetch() (
  local tag=$1 assets=$2 release_metadata ref type commit annotated
  local pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$'
  [[ $tag =~ $pattern && ${#tag} -le 64 ]] || fail 'supply an explicit canonical RC tag'
  [[ ${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-} =~ ^[0-9]{12}$ &&
    $SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID != "$demo" ]] || fail 'the publisher pin is required'
  mkdir -- "$assets" || fail 'use a new release asset directory'
  release_metadata=$(mktemp)
  trap 'rm -f -- "$release_metadata"' EXIT
  gh api "repos/$repo/releases/tags/$tag" >"$release_metadata"
  jq -e --arg tag "$tag" '
    .tag_name == $tag and .draft == false and .prerelease == true and .immutable == true and
    (.published_at | type == "string" and length > 0)
  ' <"$release_metadata" >/dev/null || fail 'the tag has no immutable public prerelease'

  ref=$(gh api "repos/$repo/git/ref/tags/$tag")
  jq -e --arg tag "$tag" '.ref == ("refs/tags/" + $tag)' <<<"$ref" >/dev/null ||
    fail 'the Git tag reference differs from the requested RC'
  type=$(jq -er '.object.type | select(. == "commit" or . == "tag")' <<<"$ref")
  commit=$(jq -er '.object.sha | strings | select(test("^[0-9a-f]{40}\\z"))' <<<"$ref")
  if [[ $type == tag ]]; then
    annotated=$(gh api "repos/$repo/git/tags/$commit")
    commit=$(jq -er --arg tag "$tag" --arg sha "$commit" '
      select(.sha == $sha and .tag == $tag and .object.type == "commit") |
      .object.sha | strings | select(test("^[0-9a-f]{40}\\z"))
    ' <<<"$annotated")
  fi
  jq -e --arg commit "$commit" '.target_commitish == $commit' <"$release_metadata" >/dev/null ||
    fail 'public release and protected Git tag point to different commits'

  gh release download "$tag" --repo "$repo" --dir "$assets" \
    --pattern distribution-manifest.json --pattern install.sh \
    --pattern shiftbox-aws-cloudformation.yaml --pattern shiftbox-aws-support.tar.gz
  [[ $(bash "$release/publish.sh" verify-draft "$tag" "$commit" "$assets" "$release_metadata") == published ]] ||
    fail 'published artifact IDs and digests did not match verified release assets'
  manifest="$assets/distribution-manifest.json"
  bash "$release/templates.sh" verify "$manifest" "$assets/shiftbox-aws-cloudformation.yaml"
  bash "$release/manifest.sh" verify-controller "$(jq -er '.controller.repository' "$manifest")" \
    "$(jq -er '.controller.digest' "$manifest")"
)

case "${1:-}:$#" in
  guard:2)
    case "$2" in qualify|cleanup) guard "$2" ;; *) fail 'invalid guard operation' ;; esac ;;
  account:1) account ;;
  capacity:2) capacity "$2" ;;
  fetch:3) fetch "$2" "$3" ;;
  *) fail 'usage: preflight.sh guard qualify|cleanup | account | capacity staffed|automated | fetch <rc> <new-assets-directory>' ;;
esac
