#!/usr/bin/env bash
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
channel=$("$here/publish.sh" check-event)
tag=${GITHUB_REF#refs/tags/}
[[ "${GITHUB_RUN_ID:-}" =~ ^[1-9][0-9]*$ ]] || { echo 'Invalid GitHub run ID.' >&2; exit 1; }

gate=aws-release-approval
runtime=aws-release-stable
if [[ "$channel" == rc ]]; then
  runtime=aws-release-candidate
fi
owner_id=11561330
environments="repos/$GITHUB_REPOSITORY/environments"

verify_environment() {
  local name=$1 tag_rule=$2 rules=$3
  gh api "$environments/$name" | jq -er --arg name "$name" --argjson rules "$rules" --argjson owner "$owner_id" '
    select(.name == $name and .deployment_branch_policy == {
      protected_branches: false, custom_branch_policies: true
    } and ([.protection_rules[].type] | sort) == $rules) |
    select(all(.protection_rules[] | select(.type == "required_reviewers");
      [.reviewers[] | {type, id: .reviewer.id}] == [{type: "User", id: $owner}])) |
    .id | select(type == "number" and . > 0)
  ' || { echo "$name must have the expected deployment protections." >&2; return 1; }
  gh api "$environments/$name/deployment-branch-policies?per_page=100" |
    jq -e --arg tag "$tag_rule" '
      .total_count == 1 and [.branch_policies[] | {name, type}] == [{name: $tag, type: "tag"}]
    ' >/dev/null || { echo "$name must allow only the configured tag rule $tag_rule." >&2; return 1; }
}

gate_id=$(verify_environment "$gate" 'v*' '["branch_policy","required_reviewers"]')
verify_environment "$runtime" "$tag" '["branch_policy"]' >/dev/null

# Require the owner review on this run and this environment ID, even if an
# environment was recreated or an administrator bypassed its waiting job.
gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/approvals" |
  jq -e --argjson owner "$owner_id" --argjson id "$gate_id" --arg name "$gate" '
    any(.[]; .state == "approved" and .user.id == $owner and
      any(.environments[]; .name == $name and .id == $id))
  ' >/dev/null || { echo 'The release run has no approval from the repository owner.' >&2; exit 1; }

printf 'Verified owner approval for %s.\n' "$tag"
