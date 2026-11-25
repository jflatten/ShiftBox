#!/usr/bin/env bash
# Exercises local payload staging and the read/write boundaries of release workflow wiring.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
candidate="$here/build-candidate.sh"
stage="$scratch/stage"
second_stage="$scratch/second-stage"
pins="$scratch/pins.json"
locations="$scratch/locations.json"
catalog="$scratch/catalog"
guides="$scratch/guides"
regions=(us-east-1 us-east-2 us-west-1 us-west-2)
tag=v2.0.1-rc.2
commit=0123456789abcdef0123456789abcdef01234567
publisher=111122223333
synthetic_hash=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
mkdir -p "$catalog" "$guides" "$scratch/bin"
for tool in aws gh docker; do
  # A local packaging task must fail the fixture if it tries to reach a cloud tool.
  cat >"$scratch/bin/$tool" <<'BLOCK'
#!/usr/bin/env bash
printf 'Unexpected external command in local packaging: %s\n' "$0" >&2
exit 91
BLOCK
  chmod +x "$scratch/bin/$tool"
done
export PATH="$scratch/bin:$PATH"

# A synthetic AMI catalog still carries the exact regional artifact ID and public provenance.
for region in "${regions[@]}"; do
  jq -n --arg region "$region" --arg tag "$tag" --arg commit "$commit" --arg hash "$synthetic_hash" '
    "3e38448b-0894-477e-807c-ca5817257064" as $run |
    {last_run_uuid: $run, builds: [{
      name: "amazon-ebs.shiftbox", builder_type: "amazon-ebs",
      packer_run_uuid: $run, artifact_id:"\($region):ami-00000000000000001",
      custom_data: {
        provider:"ec2", region:$region, architecture:"amd64", releaseId:$tag, guestAgentRelease:$tag,
        sourceRevision:$commit, sourceAmiId:"ami-00000000000000002", sourceAmiOwnerId:"099720109477",
        sourceRegion:"us-east-2", minimumRootDiskGiB:"32", platformVersion:"24.04",
        protocolVersion:"1", guestAgentSHA256:$hash, guestUpdateKeySHA256:$hash,
        toolchainRecipeSHA256:$hash, codeServerRecipeSHA256:$hash
      }
    }]}
  ' >"$catalog/$region.json"
done
for guide in install setup-helper first-workspace private-access troubleshooting contributing upgrade-recovery; do
  printf '# Public %s fixture\n' "$guide" >"$guides/$guide.md"
done
printf 'Never include this file in release artifacts.\n' >"$guides/private-evidence.md"
printf 'Never include local state.\n' >"$catalog/test.tfstate"

catalog_hashes='{}'
for region in "${regions[@]}"; do
  catalog_hashes=$(jq -c --arg region "$region" --arg hash "$(openssl dgst -sha256 -r <"$catalog/$region.json" | awk '{print $1}')" \
    '. + {($region): $hash}' <<<"$catalog_hashes")
done
jq -n --arg tag "$tag" --arg commit "$commit" --arg publisher "$publisher" \
  --arg template "$synthetic_hash" --arg installer "$synthetic_hash" --arg support "$synthetic_hash" \
  --argjson catalog "$catalog_hashes" -f "$here/tests/distribution-fixture.jq" |
  jq 'del(.template, .installer, .supportAssets) |
    .regions |= with_entries(.value |= (del(.templateLocation) |
      .workspaceImage.amiId = "ami-00000000000000001" |
      .workspaceImage.sourceAmiId = "ami-00000000000000002" |
      .workspaceImage.minimumDiskGiB = 32))' >"$pins"

# The renderer runs twice from the same pins and produces byte-identical public artifacts.
"$candidate" prepare "$pins" "$catalog" "$guides" "$stage"
"$candidate" prepare "$pins" "$catalog" "$guides" "$stage"
"$candidate" prepare "$pins" "$catalog" "$guides" "$second_stage"
for asset in install.sh shiftbox-aws-cloudformation.yaml shiftbox-aws-support.tar.gz; do
  cmp "$stage/$asset" "$second_stage/$asset"
done
grep -F 'ghcr.io/jflatten/shiftbox@sha256:' "$stage/shiftbox-aws-cloudformation.yaml" >/dev/null
grep -F "shiftbox_release_publisher='$publisher'" "$stage/install.sh" >/dev/null
bash -n "$stage/install.sh"
bash "$stage/install.sh" --help | grep -F 'Usage: install.sh' >/dev/null

# The release embeds the CloudFormation bootstrap scripts in the standalone template.
python3 - "$root/platform/cloudformation/aws" "$stage/shiftbox-aws-cloudformation.yaml" <<'PY'
import json
from pathlib import Path
import sys

source, template = map(Path, sys.argv[1:])
rendered = template.read_text()
for script in ("host/mount-data.sh", "host/mount-registry-cache.sh", "postgres/start.sh"):
    assert json.dumps((source / script).read_text()) in rendered, f"Missing embedded script: {script}"
PY

# Packaging uses a public allowlist and excludes unrelated files even in source locations.
tar -tzf "$stage/shiftbox-aws-support.tar.gz" | sort >"$scratch/actual-members"
cat >"$scratch/expected-members" <<'MEMBERS'
catalog/us-east-1.json
catalog/us-east-2.json
catalog/us-west-1.json
catalog/us-west-2.json
docs/contributing.md
docs/first-workspace.md
docs/install.md
docs/private-access.md
docs/setup-helper.md
docs/troubleshooting.md
docs/upgrade-recovery.md
setup.sh
stack-policy.json
MEMBERS
cmp "$scratch/expected-members" "$scratch/actual-members"
cmp "$catalog/us-east-2.json" <(tar -xOzf "$stage/shiftbox-aws-support.tar.gz" catalog/us-east-2.json)

# Validate the actual packaged bytes with the same static AWS tools used on release templates.
if [ "${2:-}" = --aws-tooling ]; then
  cfn-lint --regions "${regions[@]}" -- "$stage/shiftbox-aws-cloudformation.yaml"
  cfn-guard validate --rules "$root/platform/cloudformation/aws/shiftbox.guard" \
    --data "$stage/shiftbox-aws-cloudformation.yaml"
  python3 "$root/platform/qualification/policy-template.test.py" "$stage/shiftbox-aws-cloudformation.yaml"
fi

# The exact template checksum and original object versions persist through stable promotion.
template_hash=$(openssl dgst -sha256 -r <"$stage/shiftbox-aws-cloudformation.yaml" | awk '{print $1}')
jq -n --arg tag "$tag" --arg hash "$template_hash" '
  reduce ("us-east-1", "us-east-2", "us-west-1", "us-west-2") as $region ({};
    .[$region] = {
      bucket: "fixture-publisher-\($region)",
      key: "public/templates/\($tag)/\($hash)/shiftbox-aws-cloudformation.yaml",
      versionId: "pin+constructed/version="
    })
' >"$locations"
"$candidate" finalize "$pins" "$locations" "$stage"
cp "$stage/distribution-manifest.json" "$scratch/approved.json"
"$candidate" finalize "$pins" "$locations" "$stage"
cmp "$scratch/approved.json" "$stage/distribution-manifest.json"
"$here/manifest.sh" validate "$stage/distribution-manifest.json" "$tag" "$commit" "$publisher"
"$here/manifest.sh" validate "$stage/distribution-manifest.json" v2.0.1 "$commit" "$publisher"
"$here/manifest.sh" verify-assets "$stage/distribution-manifest.json" "$stage"
jq -e --slurpfile pinned "$locations" '
  all(.regions | to_entries[];
     .value.templateLocation.versionId == $pinned[0][.key].versionId and
     (.value.templateLocation.quickCreateUrl | contains("pin%252Bconstructed%252Fversion%253D")))
' "$stage/distribution-manifest.json" >/dev/null

expect_rejected() {
  local name=$1
  shift
  if "$@" >"$scratch/rejected.log" 2>&1; then
    printf 'The rejected fixture succeeded: %s\n' "$name" >&2
    exit 1
  fi
}

# A digest mutation, wrong object key or replacement version must not rewrite an approved stage.
jq '.["us-east-2"].versionId = "different-original"' "$locations" >"$scratch/mismatched-version.json"
expect_rejected 'replacement hosted S3 version' "$candidate" finalize "$pins" "$scratch/mismatched-version.json" "$stage"
cmp "$scratch/approved.json" "$stage/distribution-manifest.json"
jq '.["us-west-2"].key = "public/templates/latest.yaml"' "$locations" >"$scratch/mismatched-key.json"
expect_rejected 'unverified object key' "$candidate" finalize "$pins" "$scratch/mismatched-key.json" "$second_stage"
jq '.["us-west-2"].bucket = .["us-east-2"].bucket' "$locations" >"$scratch/repeated-bucket.json"
expect_rejected 'the same bucket cannot be the release bucket in two regions' \
  "$candidate" finalize "$pins" "$scratch/repeated-bucket.json" "$second_stage"
printf '\n# edited after preparation\n' >>"$stage/shiftbox-aws-cloudformation.yaml"
expect_rejected 'changed template bytes' "$candidate" finalize "$pins" "$locations" "$stage"
cmp "$scratch/approved.json" "$stage/distribution-manifest.json"

# Malformed version strings and misbound catalog provenance never produce release assets.
for bad_tag in v02.0.1-rc.2 v2.0.1-rc.0 v2.0.1; do
  jq --arg bad "$bad_tag" '.candidateTag = $bad | .runtimeCompatibility = $bad' "$pins" >"$scratch/bad-pins.json"
  expect_rejected "invalid candidate $bad_tag" "$candidate" prepare "$scratch/bad-pins.json" "$catalog" "$guides" "$scratch/bad-stage"
done
jq '.regions["us-east-2"].workspaceImage.amiId = "ami-00000000000000009"' "$pins" >"$scratch/bad-pins.json"
expect_rejected 'misbound AMI catalog' "$candidate" prepare "$scratch/bad-pins.json" "$catalog" "$guides" "$scratch/bad-stage"

printf 'CloudFormation candidate packaging tests passed.\n'
if [ "${1:-}" = --packaging-only ]; then
  exit 0
fi

# Same-run handoffs are addressed by ID and ambiguity or missing provenance must fail closed.
handoffs="$here/candidate-handoffs.jq"
jq -n --arg tag "$tag" '[
  {artifacts:[{id:7,name:("ami-build-"+$tag),expired:false}]},
  {artifacts:[{id:8,name:("ami-copy-"+$tag),expired:false},{id:9,name:"unrelated",expired:false}]}
]' >"$scratch/handoff-pages.json"
printf 'build=7\ncopy=8\nshare=\nassets=\n' >"$scratch/expected-handoffs"
jq -er --arg tag "$tag" -f "$handoffs" "$scratch/handoff-pages.json" >"$scratch/actual-handoffs"
cmp "$scratch/expected-handoffs" "$scratch/actual-handoffs"
for bad in '.[0].artifacts = []' '.[0].artifacts += .[0].artifacts' '.[0].artifacts[0].expired = true' '.[1] = {}'; do
  jq "$bad" "$scratch/handoff-pages.json" >"$scratch/bad-handoffs"
  expect_rejected 'ambiguous or missing handoff' jq -er --arg tag "$tag" -f "$handoffs" "$scratch/bad-handoffs"
done

# A complete frozen candidate still restores after intermediate artifacts age out.
jq -n --arg tag "$tag" '[{artifacts:[{id:10,name:("release-assets-"+$tag),expired:false}]}]' |
  jq -er --arg tag "$tag" -f "$handoffs" | grep -Fx 'assets=10' >/dev/null

# Exercise the approval gate against GitHub API-shaped fixtures without authenticating.
(
  export GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_EVENT_NAME=push GITHUB_REF_TYPE=tag
  export GITHUB_REF_PROTECTED=true GITHUB_RUN_ID=100 GITHUB_ACTOR=jflatten
  export GITHUB_TRIGGERING_ACTOR=jflatten SHIFTBOX_RELEASE_MAINTAINERS=jflatten
  export GITHUB_REF="refs/tags/$tag"
  export APPROVAL_FIXTURES="$scratch/approval"
  mkdir "$APPROVAL_FIXTURES"
  cat >"$scratch/bin/gh" <<'GH'
#!/usr/bin/env bash
case "$*" in
  'api repos/jflatten/ShiftBox/actions/runs/100/approvals') name=approvals ;;
  'api repos/jflatten/ShiftBox/environments/aws-release-approval') name=gate ;;
  'api repos/jflatten/ShiftBox/environments/aws-release-'candidate|'api repos/jflatten/ShiftBox/environments/aws-release-'stable) name=runtime ;;
  'api repos/jflatten/ShiftBox/environments/aws-release-approval/deployment-branch-policies?per_page=100') name=gate-tags ;;
  'api repos/jflatten/ShiftBox/environments/aws-release-'*/'deployment-branch-policies?per_page=100') name=runtime-tags ;;
  *) printf 'Unexpected approval fixture request: %s\n' "$*" >&2; exit 91 ;;
esac
cat "$APPROVAL_FIXTURES/$name"
GH
  printf '%s\n' '{"id":50,"name":"aws-release-approval","protection_rules":[{"type":"required_reviewers","reviewers":[{"type":"User","reviewer":{"id":11561330}}]},{"type":"branch_policy"}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}' >"$APPROVAL_FIXTURES/gate"
  printf '%s\n' '{"id":51,"name":"aws-release-candidate","protection_rules":[{"type":"branch_policy"}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}' >"$APPROVAL_FIXTURES/runtime"
  printf '%s\n' '{"total_count":1,"branch_policies":[{"name":"v*","type":"tag"}]}' >"$APPROVAL_FIXTURES/gate-tags"
  jq -n --arg tag "$tag" '{total_count:1,branch_policies:[{name:$tag,type:"tag"}]}' >"$APPROVAL_FIXTURES/runtime-tags"
  printf '%s\n' '[{"state":"approved","user":{"id":11561330},"environments":[{"name":"aws-release-approval","id":50}]}]' >"$APPROVAL_FIXTURES/approvals"
  approve=(bash "$here/verify-approval.sh")

  # The owner's review unlocks the RC with the exact approved runtime tag.
  "${approve[@]}"
  check_rejected_approval() {
    local file=$1 change=$2
    cp "$APPROVAL_FIXTURES/$file" "$scratch/original"
    jq "$change" "$scratch/original" >"$APPROVAL_FIXTURES/$file"
    expect_rejected "$file $change" "${approve[@]}"
    mv "$scratch/original" "$APPROVAL_FIXTURES/$file"
  }

  # Fail closed after an unprotected auto-creation, a missing or invalid review,
  # a lingering phase reviewer, or a tag policy admitting legacy workflows.
  check_rejected_approval gate '.protection_rules = []'
  check_rejected_approval gate '.protection_rules[0].reviewers[0].reviewer.id = 42'
  check_rejected_approval gate-tags '.branch_policies[0].type = "branch"'
  check_rejected_approval approvals '[]'
  check_rejected_approval approvals '.[0].user.id = 42'
  check_rejected_approval approvals '.[0].environments[0].id = 49'
  check_rejected_approval approvals '.[0].state = "rejected"'
  check_rejected_approval runtime '.protection_rules += [{type:"required_reviewers"}]'
  check_rejected_approval runtime-tags '.branch_policies[0].name = "v*"'
  check_rejected_approval runtime-tags '.total_count = 2'

  # The stable release uses the same approval and checks its own execution environment.
  export GITHUB_REF=refs/tags/v2.0.1
  jq '.name = "aws-release-stable"' "$APPROVAL_FIXTURES/runtime" >"$scratch/stable-env"
  mv "$scratch/stable-env" "$APPROVAL_FIXTURES/runtime"
  jq -n '{total_count:1,branch_policies:[{name:"v2.0.1",type:"tag"}]}' >"$APPROVAL_FIXTURES/runtime-tags"
  "${approve[@]}"
)

python3 - "$root" <<'PY'
from pathlib import Path
import sys
import yaml

workflows = Path(sys.argv[1]) / ".github/workflows"


def workflow(filename):
    """Read Actions YAML without interpreting the trigger key on as a boolean."""
    return yaml.load((workflows / filename).read_text(), Loader=yaml.BaseLoader)


def writable(permissions):
    """Detect write authorization regardless of whether Actions uses map or scalar syntax."""
    return permissions == "write-all" or (
        isinstance(permissions, dict) and any(access == "write" for access in permissions.values())
    )


# Ordinary checks run automatically on PRs and can be dispatched for an exact release commit.
ci = workflow("ci.yml")
assert set(ci["on"]) == {"pull_request", "workflow_dispatch"}, "Checks must avoid duplicate push runs"

# Ordinary checks never gain publication or AWS writer authority.
assert not writable(ci["permissions"])
assert all(not writable(job.get("permissions", {})) for job in ci["jobs"].values())

# CodeQL scans PRs to main without repeating the scan on push.
codeql = workflow("codeql.yml")
assert codeql["on"] == {"pull_request": {"branches": ["main"]}}, "CodeQL must run only on PRs to main"

# Production publication is triggered by version tags only and the default token is read-only.
release = workflow("release.yml")
assert set(release["on"]) == {"push"}
assert set(release["on"]["push"]) == {"tags"}
assert release["on"]["push"]["tags"]
assert not writable(release.get("permissions", {}))

# Publisher and stable verification assume separate roles after a prerequisite gate.
aws_jobs = []
for name, job in release["jobs"].items():
    for step in job["steps"]:
        if step.get("uses", "").startswith("aws-actions/configure-aws-credentials@"):
            assert job.get("permissions", {}).get("id-token") == "write", name
            assert job.get("needs"), name
            aws_jobs.append((name, step["with"]["role-to-assume"]))
assert len({role for _, role in aws_jobs}) >= 2, aws_jobs

# One read-only gate covers all release jobs, including retry paths that can
# run when another dependency is skipped. The runtime OIDC environments stay separate.
jobs = release["jobs"]
gate = jobs["approve-release"]
assert gate["environment"] == "aws-release-approval"
assert gate["needs"] == "preflight"
assert gate["permissions"] == {"contents": "read", "actions": "read"}
assert any(step.get("run") == "bash platform/release/verify-approval.sh" for step in gate["steps"])
assert [name for name, job in jobs.items() if job.get("environment") == gate["environment"]] == ["approve-release"]
for name, job in jobs.items():
    if name not in {"preflight", "approve-release"}:
        assert "approve-release" in job["needs"], name
        assert "needs.approve-release.result == 'success'" in job["if"], name

# Each AMI phase needs a fresh role session after the shared approval.
# Retry inputs come from immutable same-run artifacts rather than re-running Packer.
phases = {"candidate-ami-build": "hosted-build.sh", "candidate-ami-copy": "amis.sh prepare",
          "candidate-ami-share": "amis.sh share"}
for name, operation in phases.items():
    job = jobs[name]
    commands = "\n".join(step.get("run", "") for step in job["steps"])
    assert job["environment"] == "aws-release-candidate"
    assert job["permissions"] == {"contents": "read", "actions": "read", "id-token": "write"}
    assert "candidate-setup" in job["needs"] and operation in commands
    assert all(other not in commands for other in phases.values() if other != operation)
    assert (name, "${{ vars.SHIFTBOX_RELEASE_NETWORK_ROLE_ARN }}") in aws_jobs
    assert (name, "${{ steps.phase.outputs.role }}") in aws_jobs
    open_index, = [index for index, step in enumerate(job["steps"]) if step.get("id") == "phase"]
    action_index, = [index for index, step in enumerate(job["steps"]) if operation in step.get("run", "")]
    close_index, = [index for index, step in enumerate(job["steps"]) if "release-phase.sh close" in step.get("run", "")]
    assert open_index < action_index < close_index
    assert "always()" in job["steps"][close_index]["if"]
    upload, = [step["with"] for step in job["steps"] if step.get("uses", "").startswith("actions/upload-artifact@")]
    assert upload.get("overwrite", "false") == "false"
    if name != "candidate-ami-build":
        assert job["runs-on"] == "ubuntu-latest"
        assert all(step["with"].get("artifact-ids") for step in job["steps"]
                   if step.get("uses", "").startswith("actions/download-artifact@"))

# The protected build owns a disposable network and passes its runner-only key
# to Packer. Both success and failure paths attempt to retire the exact state.
assert jobs["candidate-ami-build"]["runs-on"] == "ubuntu-24.04"
build_steps = jobs["candidate-ami-build"]["steps"]
phase_check_index, = [index for index, step in enumerate(build_steps) if "release-phase.sh check" in step.get("run", "")]
provision_index, = [index for index, step in enumerate(build_steps) if "release-network.sh provision" in step.get("run", "")]
build_index, = [index for index, step in enumerate(build_steps) if "hosted-build.sh" in step.get("run", "")]
retire_index, = [index for index, step in enumerate(build_steps) if "release-network.sh retire" in step.get("run", "")]
# Check every delegated role name with the control session before creating egress.
control_index, = [index for index, step in enumerate(build_steps) if step.get("id") == "control"]
assert control_index < phase_check_index < provision_index < build_index < retire_index
assert "for phase in build copy share" in build_steps[phase_check_index]["run"]
assert "always()" in build_steps[retire_index]["if"]
assert "shiftbox-publisher-network/packer" in build_steps[build_index]["run"]
assert "secrets.SHIFTBOX_RELEASE_BUILD_SSH_KEY" not in (workflows / "release.yml").read_text()
assert "SHIFTBOX_RELEASE_CANDIDATE_RUNNER" not in (workflows / "release.yml").read_text()
assert "outputs.build == ''" in jobs["candidate-ami-build"]["if"]
assert "candidate-ami-build" in jobs["candidate-ami-copy"]["needs"]
assert "candidate-ami-copy" in jobs["candidate-ami-share"]["needs"]
assert "candidate-ami-share" in jobs["candidate"]["needs"]
assert ("candidate", "${{ vars.SHIFTBOX_RELEASE_NETWORK_ROLE_ARN }}") not in aws_jobs
assert jobs["candidate"]["runs-on"] == "ubuntu-latest"

# An independent protected main workflow can recover expired state even if
# the build runner disappeared.
cleanup = workflow("cleanup-release-network.yml")

# The live rehearsal has one protected job that shares the release Terraform
# engine, retains its always-run teardown, and has no publication permission.
rehearsal = workflow("rehearse-release-network.yml")
assert set(rehearsal["on"]) == {"workflow_dispatch"}
assert rehearsal["permissions"] == {}
assert set(rehearsal["jobs"]) == {"network"}
network = rehearsal["jobs"]["network"]
assert network["environment"] == "aws-release-network-rehearsal"
assert network["permissions"] == {"actions": "read", "contents": "read", "id-token": "write"}
assert rehearsal["concurrency"]["group"] == "shiftbox-release-network-rehearsal"
assert rehearsal["concurrency"]["cancel-in-progress"] == "false"
assert "github.ref_protected" in network["if"] and "github.run_attempt == '1'" in network["if"]
commands = "\n".join(step.get("run", "") for step in network["steps"])
assert "release-network.sh provision" in commands and "release-network.sh retire" in commands
assert not any(command in commands for command in ("publish.sh", "hosted-build.sh", "release-phase.sh", "amis.sh"))
audit, oidc, provision, retire = [next(i for i, step in enumerate(network["steps"]) if step.get("id") == id)
                                  for id in ("preflight", "aws", "provision", "retire")]
assert audit < oidc < provision < retire
assert network["steps"][retire]["if"] == "always() && steps.aws.outcome == 'success'"
assert not any("secrets." in str(step) for step in network["steps"])
assert set(cleanup["on"]) == {"schedule", "workflow_dispatch"}
assert cleanup["concurrency"]["cancel-in-progress"] == "false"
assert not writable(cleanup["permissions"])
cleanup_job = cleanup["jobs"]["cleanup"]
assert cleanup_job["environment"] == "aws-release-cleanup"
assert cleanup_job["permissions"] == {"actions": "read", "contents": "read", "id-token": "write"}
for gate in ("refs/heads/main", "SHIFTBOX_RELEASE_CLEANUP_ENABLED", "jflatten/ShiftBox", "jflatten"):
    assert gate in cleanup_job["if"]
cleanup_aws, = [step["with"] for step in cleanup_job["steps"]
                if step.get("uses", "").startswith("aws-actions/configure-aws-credentials@")]
assert cleanup_aws["role-to-assume"] == "${{ vars.SHIFTBOX_RELEASE_NETWORK_CLEANUP_ROLE_ARN }}"
assert cleanup_aws["allowed-account-ids"] == "${{ vars.SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID }}"
cleanup_commands = "\n".join(step.get("run", "") for step in cleanup_job["steps"])
assert "recover-release-network.sh" in cleanup_commands and "release-phase.sh sweep" in cleanup_commands
assert "hosted-build.sh" not in cleanup_commands

# A first GHCR push can require a separate visibility approval. Freeze the
# exact candidate before testing anonymous layers so its protected rerun can resume.
candidate_steps = release["jobs"]["candidate"]["steps"]
# Digest-only pushing requires an explicit container builder in the same
# temporary Docker configuration as the authenticated registry export.
controller, = [step for step in candidate_steps
               if step.get("name") == "Build the full controller and record its registry digest"]
controller_commands = controller["run"]
# The native helper is produced from the same source and tied to an immutable
# same-run artifact ID before the Linux image can be built.
helper = jobs["candidate-helper"]
assert helper["runs-on"] == "macos-15"
assert helper["permissions"] == {"contents": "read"}
# The native build uploads and checksums the same disk image the controller embeds.
assert any("shasum -a 256 ShiftBox-Local-Helper.dmg" in step.get("run", "") for step in helper["steps"])
assert any("macos-helper/ShiftBox-Local-Helper.dmg" in step.get("with", {}).get("path", "") for step in helper["steps"])
assert "candidate-helper" in jobs["candidate"]["needs"]
assert "needs.candidate-helper.result == 'success'" in jobs["candidate"]["if"]
assert any(step.get("with", {}).get("artifact-ids") == "${{ needs.candidate-helper.outputs.artifact }}" for step in candidate_steps)
assert '--build-context "macos-helper=$RUNNER_TEMP/candidate/helper"' in controller_commands
assert 'shasum -a 256 -c SHA256SUMS' in controller_commands
assert controller["if"] == "steps.restore.outputs.assets == ''"
assert 'export DOCKER_CONFIG="$registry_config" BUILDX_CONFIG="$registry_config/buildx"' in controller_commands
create_builder = 'docker buildx create --name "$builder" --driver docker-container --bootstrap'
push_digest = 'docker buildx build --builder "$builder" --platform linux/amd64'
assert controller_commands.index(create_builder) < controller_commands.index(push_digest)
assert 'docker buildx rm "$builder"' in controller_commands and 'trap cleanup EXIT' in controller_commands
assert 'push-by-digest=true,name-canonical=true,push=true' in controller_commands
stage_index, = [index for index, step in enumerate(candidate_steps)
                if step.get("uses", "").startswith("actions/upload-artifact@")]
registry_index, = [index for index, step in enumerate(candidate_steps)
                   if "manifest.sh verify-controller" in step.get("run", "")]
assert registry_index > stage_index
assert "pull --platform linux/amd64" in candidate_steps[registry_index]["run"]
assert "if" not in candidate_steps[registry_index]
assert "needs.candidate.result == 'success'" in release["jobs"]["publish"]["if"]

# Stable promotion uses its own verification path rather than following the candidate build path.
publish = [name for name, job in release["jobs"].items()
           if any("platform/release/publish.sh" in step.get("run", "") for step in job["steps"])]
promote = [name for name, job in release["jobs"].items()
           if any("platform/release/promote.sh" in step.get("run", "") for step in job["steps"])]
assert publish and promote and set(publish).isdisjoint(promote)
for name in promote:
    assert all("build-candidate.sh prepare" not in step.get("run", "") and
               "templates.sh upload" not in step.get("run", "")
               for step in release["jobs"][name]["steps"]), name
print("Release workflow authority tests passed.")
PY
