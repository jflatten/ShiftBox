#!/usr/bin/env bash
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
manifest_tool="$here/manifest.sh"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
export COPYFILE_DISABLE=1

tag=v1.4.2-rc.1
source_commit=0123456789abcdef0123456789abcdef01234567
publisher=111122223333
manifest="$scratch/distribution.json"
mutated="$scratch/mutated.json"
assets="$scratch/assets"
support="$scratch/support"
regions=(us-east-1 us-east-2 us-west-1 us-west-2)
passed=0
failed=0

expect_ok() {
  local description=$1
  shift
  if "$@" >"$scratch/stdout" 2>"$scratch/stderr"; then
    passed=$((passed + 1))
    printf 'ok - %s\n' "$description"
  else
    failed=$((failed + 1))
    printf 'not ok - %s\n' "$description" >&2
    cat "$scratch/stderr" >&2
  fi
}

expect_rejected() {
  local description=$1 status=0
  shift
  "$@" >"$scratch/stdout" 2>"$scratch/stderr" || status=$?
  if [ "$status" -eq 0 ] || [ "$status" -ge 126 ]; then
    failed=$((failed + 1))
    printf 'not ok - %s (status %s)\n' "$description" "$status" >&2
    cat "$scratch/stderr" >&2
  else
    passed=$((passed + 1))
    printf 'ok - %s\n' "$description"
  fi
}

reject_manifest_edit() {
  local description=$1 filter=$2
  jq "$filter" "$manifest" >"$mutated"
  expect_rejected "$description" "$manifest_tool" validate "$mutated" "$tag" "$source_commit" "$publisher"
}

sha256() {
  shasum -a 256 "$1" | awk '{print $1}'
}

mkdir -p "$assets" "$support/catalog" "$support/docs"
printf 'Resources: {}\n' >"$assets/shiftbox-aws-cloudformation.yaml"
printf '#!/bin/sh\n' >"$assets/install.sh"
printf '{"Statement":[]}\n' >"$support/stack-policy.json"
printf '#!/bin/sh\n' >"$support/setup.sh"
printf '# Fixture operator guide\n' >"$support/docs/install.md"
catalog_hashes='{}'
for region in "${regions[@]}"; do
  jq -n --arg region "$region" '{region: $region}' >"$support/catalog/$region.json"
  catalog_hashes=$(jq -c --arg region "$region" --arg hash "$(sha256 "$support/catalog/$region.json")" \
    '. + {($region): $hash}' <<<"$catalog_hashes")
done
tar -C "$support" -czf "$assets/shiftbox-aws-support.tar.gz" catalog docs stack-policy.json setup.sh
jq -n --arg tag "$tag" --arg commit "$source_commit" --arg publisher "$publisher" \
  --arg template "$(sha256 "$assets/shiftbox-aws-cloudformation.yaml")" \
  --arg installer "$(sha256 "$assets/install.sh")" \
  --arg support "$(sha256 "$assets/shiftbox-aws-support.tar.gz")" \
  --argjson catalog "$catalog_hashes" -f "$here/tests/distribution-fixture.jq" >"$manifest"
release_assets="$scratch/release-assets"
mkdir "$release_assets"
cp "$assets/"* "$release_assets/"
cp "$manifest" "$release_assets/distribution-manifest.json"

if [ ! -x "$manifest_tool" ]; then
  printf 'Release manifest helper is missing or not executable: %s\n' "$manifest_tool" >&2
  exit 1
fi

# An RC and its same-core stable promotion consume the identical manifest bytes.
expect_ok 'validate a complete RC with four regional bindings' \
  "$manifest_tool" validate "$manifest" "$tag" "$source_commit" "$publisher"
expect_ok 'validate the same payload for the matching stable tag' \
  "$manifest_tool" validate "$manifest" v1.4.2 "$source_commit" "$publisher"
expect_ok 'verify byte checksums and all required catalog members' \
  "$manifest_tool" verify-assets "$manifest" "$assets"
expect_ok 'accept the fixed support layout with directory records' \
  "$manifest_tool" validate-support "$assets/shiftbox-aws-support.tar.gz"

# Requested source identity and strict Git tags govern whether a candidate is eligible.
expect_rejected 'reject another RC with the same core' \
  "$manifest_tool" validate "$manifest" v1.4.2-rc.2 "$source_commit" "$publisher"
expect_rejected 'reject stable with a different core' \
  "$manifest_tool" validate "$manifest" v1.4.3 "$source_commit" "$publisher"
expect_rejected 'reject a release tag containing a leading zero' \
  "$manifest_tool" validate "$manifest" v01.4.2 "$source_commit" "$publisher"
expect_rejected 'reject RC number zero' \
  "$manifest_tool" validate "$manifest" v1.4.2-rc.0 "$source_commit" "$publisher"
expect_rejected 'reject an unrelated independently observed source commit' \
  "$manifest_tool" validate "$manifest" "$tag" 1111111111111111111111111111111111111111 "$publisher"
expect_rejected 'reject a different independently approved publisher' \
  "$manifest_tool" validate "$manifest" "$tag" "$source_commit" 999988887777
reject_manifest_edit 'reject an unknown distribution schema version' '.schemaVersion = 2'
reject_manifest_edit 'reject a stable label in the embedded candidate field' '.candidateTag = "v1.4.2"'
reject_manifest_edit 'reject stable as a substitute runtime compatibility identity' '.runtimeCompatibility = "v1.4.2"'

# Customers consume only the approved digest, publisher metadata and exact regional descriptors.
reject_manifest_edit 'reject an unrelated controller repository' '.controller.repository = "ghcr.io/other/shiftbox"'
reject_manifest_edit 'reject a tagged controller instead of a digest' '.controller.digest = "latest"'
reject_manifest_edit 'reject an unexpected controller platform' '.controller.platform = "linux/arm64"'
reject_manifest_edit 'reject a floating PostgreSQL image' '.postgresImage = "docker.io/library/postgres:17"'
reject_manifest_edit 'reject a foreign PostgreSQL image even when its digest is pinned' '.postgresImage |= sub("library/postgres"; "other/postgres")'
reject_manifest_edit 'reject changing the template asset filename' '.template.name = "other-template.yaml"'
reject_manifest_edit 'reject a malformed template SHA-256' '.template.sha256 = "checksum"'
reject_manifest_edit 'reject an absent supported region' 'del(.regions["us-west-1"])'
reject_manifest_edit 'reject an extra region' '.regions["eu-west-1"] = .regions["us-west-1"]'
reject_manifest_edit 'reject a malformed ECS host AMI ID' '.regions["us-east-2"].hostAmiId = "ami-invalid"'
reject_manifest_edit 'reject an incompatible workspace disk minimum' '.regions["us-east-2"].workspaceImage.minimumDiskGiB = 8'
reject_manifest_edit 'reject a guest release mismatch' '.regions["us-east-2"].workspaceImage.guestRelease = "development"'
reject_manifest_edit 'reject a catalog member outside the fixed layout' '.regions["us-east-2"].workspaceImage.catalogPath = "../us-east-2.json"'

# The region, exact bucket/key/version and console URL must bind to one verified template.
reject_manifest_edit 'reject a mismatched regional checksum' '.regions["us-east-2"].templateLocation.sha256 = ("0" * 64)'
reject_manifest_edit 'reject a key without the template hash' '.regions["us-east-2"].templateLocation.key = "public/templates/latest.yaml"'
reject_manifest_edit 'reject an empty object version' '.regions["us-east-2"].templateLocation.versionId = ""'
reject_manifest_edit 'reject a different object version in the S3 URL' '.regions["us-east-2"].templateLocation.url |= sub("versionId=.*"; "versionId=other")'
reject_manifest_edit 'reject a copied quick-create link from another region' '.regions["us-east-2"].templateLocation.quickCreateUrl = .regions["us-west-1"].templateLocation.quickCreateUrl'
reject_manifest_edit 'reject a quick-create link with broken percent encoding' '.regions["us-east-2"].templateLocation.quickCreateUrl |= sub("%25"; "%")'
location=$(jq -r '.regions["us-east-2"].templateLocation.url' "$manifest")
expect_ok 'generate a canonical quick-create link for an encoded S3 object version' \
  "$manifest_tool" quick-create us-east-2 "$location"
expected=$(jq -r '.regions["us-east-2"].templateLocation.quickCreateUrl' "$manifest")
if [ "$(cat "$scratch/stdout")" != "$expected" ]; then
  failed=$((failed + 1))
  printf 'not ok - quick-create URL encodes the full versioned S3 URL exactly once\n' >&2
fi
expect_rejected 'reject a regional S3 endpoint mismatch in a generated console link' \
  "$manifest_tool" quick-create us-west-1 "$location"
expect_rejected 'reject constructing a quick-create link for an unversioned S3 URL' \
  "$manifest_tool" quick-create us-east-2 "${location%%\?*}"

# Downloads must match the manifest, including the regional catalog inside a rehashed archive.
printf '\n# changed after approval\n' >>"$assets/shiftbox-aws-cloudformation.yaml"
expect_rejected 'reject altered downloaded template bytes' \
  "$manifest_tool" verify-assets "$manifest" "$assets"
printf 'Resources: {}\n' >"$assets/shiftbox-aws-cloudformation.yaml"
printf '{"region":"changed"}\n' >"$support/catalog/us-east-2.json"
tar -C "$support" -czf "$assets/shiftbox-aws-support.tar.gz" catalog docs stack-policy.json setup.sh
jq --arg hash "$(sha256 "$assets/shiftbox-aws-support.tar.gz")" '.supportAssets.sha256 = $hash' \
  "$manifest" >"$mutated"
expect_rejected 'reject changed regional catalog even when the outer archive hash matches' \
  "$manifest_tool" verify-assets "$mutated" "$assets"

# Invalid archives model accidental packaging, duplicate names, traversal and unsafe file types.
python3 - "$support" "$scratch" <<'PY'
import io
from pathlib import Path
import sys
import tarfile

support, scratch = map(Path, sys.argv[1:])
for name, addition in (
    ("private", "private/example.log"),
    ("traversal", "../outside"),
    ("absolute", "/tmp/outside"),
    ("duplicate", "catalog/us-east-2.json"),
    ("symlink", "docs/outside.md"),
    ("hardlink", "docs/alias.md"),
    ("missing-catalog", None),
):
    with tarfile.open(scratch / f"{name}.tar.gz", "w:gz") as archive:
        for member in sorted(path for path in support.rglob("*") if path.is_file()):
            path = member.relative_to(support).as_posix()
            if name != "missing-catalog" or path != "catalog/us-east-2.json":
                archive.add(member, arcname=path)
        if addition is None:
            continue
        entry = tarfile.TarInfo(addition)
        if name in {"symlink", "hardlink"}:
            entry.type = tarfile.SYMTYPE if name == "symlink" else tarfile.LNKTYPE
            entry.linkname = "docs/install.md"
            archive.addfile(entry)
        else:
            entry.size = 1
            archive.addfile(entry, io.BytesIO(b"x"))
PY
for invalid in private traversal absolute duplicate symlink hardlink missing-catalog; do
  expect_rejected "reject an unsafe support archive ($invalid)" \
    "$manifest_tool" validate-support "$scratch/$invalid.tar.gz"
done

# Only an explicitly approved maintainer's protected tag push can enter publication.
publisher_tool="$here/publish.sh"
promote_tool="$here/promote.sh"
release_event() {
  env GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_EVENT_NAME=push GITHUB_REF_TYPE=tag \
    GITHUB_REF="refs/tags/$tag" GITHUB_REF_PROTECTED=true GITHUB_ACTOR=maintainer \
    GITHUB_TRIGGERING_ACTOR=maintainer SHIFTBOX_RELEASE_MAINTAINERS=maintainer,other \
    "$@" "$publisher_tool" check-event
}
expect_ok 'accept an approved protected RC tag push' release_event
expect_rejected 'reject an unprotected version tag' release_event GITHUB_REF_PROTECTED=false
expect_rejected 'reject a release run initiated by a pull request' release_event GITHUB_EVENT_NAME=pull_request
expect_rejected 'reject a branch ref impersonating a tag' release_event GITHUB_REF=refs/heads/main
expect_rejected 'reject a workflow from an unexpected repository' release_event GITHUB_REPOSITORY=example/ShiftBox
expect_rejected 'reject an unapproved tag actor' release_event GITHUB_ACTOR=guest
expect_rejected 'reject an unapproved workflow rerun actor' release_event GITHUB_TRIGGERING_ACTOR=guest
expect_rejected 'reject a tag outside the official version syntax' release_event GITHUB_REF=refs/tags/v1.4.2-rc.01

# A tag's peeled commit must match both the push and the reviewed main lineage.
source_repo="$scratch/source"
git init -q "$source_repo"
git -C "$source_repo" config commit.gpgSign false
git -C "$source_repo" config tag.gpgSign false
git -C "$source_repo" -c user.name='Release fixture' -c user.email='fixture@example.test' commit -qm reviewed --allow-empty
reviewed=$(git -C "$source_repo" rev-parse HEAD)
git -C "$source_repo" update-ref refs/remotes/origin/main "$reviewed"
git -C "$source_repo" tag "$tag" "$reviewed"
git -C "$source_repo" -c user.name='Release fixture' -c user.email='fixture@example.test' commit -qm pending --allow-empty
unreviewed=$(git -C "$source_repo" rev-parse HEAD)
git -C "$source_repo" tag v1.4.2-rc.2 "$unreviewed"
check_source() { (cd "$source_repo" && "$publisher_tool" check-source "$@"); }
expect_ok 'accept the exact tag commit reachable from reviewed main' check_source "$tag" "$reviewed"
expect_rejected 'reject a tag that moved since the protected push' check_source "$tag" "$unreviewed"
expect_rejected 'reject a candidate not reachable from reviewed main' check_source v1.4.2-rc.2 "$unreviewed"

# An interrupted draft may reuse only bytes bound to the original manifest.
release_json="$scratch/rc-release.json"
partial_json="$scratch/partial-release.json"
draft_marker="<!-- shiftbox-release manifest-sha256=$(sha256 "$manifest") source=$source_commit -->"
jq -n --arg tag "$tag" --arg commit "$source_commit" --arg marker "$draft_marker" --arg manifest "$(sha256 "$manifest")" \
  --slurpfile payload "$manifest" '
  {id:9001,tag_name:$tag,target_commitish:$commit,body:$marker,prerelease:true,draft:false,immutable:true,
   assets: ([$payload[0].installer, $payload[0].template, $payload[0].supportAssets,
       {name:"distribution-manifest.json",sha256:$manifest}] | to_entries |
     map({id:(1000+.key),name:.value.name,digest:("sha256:"+.value.sha256),state:"uploaded"}))}' >"$release_json"
jq '.draft = true | .immutable = false | .assets |= .[:2]' "$release_json" >"$partial_json"
check_draft() {
  SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID="$publisher" "$publisher_tool" verify-draft "$tag" "$source_commit" "$release_assets" "$1"
}
expect_ok 'resume a partial draft with the authorized manifest marker' check_draft "$partial_json"
expect_ok 'recognize a completed immutable release with the same assets' check_draft "$release_json"
for edit in '.body = "wrong marker"' '.assets[0].digest = "sha256:bad"' \
  '.assets[0].name = "unplanned-asset.bin"' '.tag_name = "v1.4.2-rc.2"'; do
  jq "$edit" "$partial_json" >"$mutated"
  expect_rejected "reject partial draft identity drift ($edit)" check_draft "$mutated"
done
jq '.immutable = false' "$release_json" >"$mutated"
expect_rejected 'reject a mutable release as completed evidence' check_draft "$mutated"
jq '.assets |= .[:3]' "$release_json" >"$mutated"
expect_rejected 'reject a published release with a missing asset' check_draft "$mutated"
expect_ok 'allow a stable update above the current default' "$publisher_tool" check-stable-latest v1.10.0 v1.9.9
expect_rejected 'reject an older stable rerun that would move the default backward' \
  "$publisher_tool" check-stable-latest v1.9.9 v1.10.0

# Stable promotion requires one exact immutable RC and every independently recorded gate.
qualification="$scratch/qualification.json"
jq -n --slurpfile rc "$release_json" --slurpfile payload "$manifest" '
  $rc[0] as $rc | $payload[0] as $payload |
  {schemaVersion: 1,
   candidate: {tag:$rc.tag_name,sourceCommit:$payload.sourceCommit,releaseId:$rc.id,
     assets:($rc.assets | map({key:.name,value:{id,sha256:(.digest | ltrimstr("sha256:"))}}) | from_entries),
     controllerDigest:$payload.controller.digest,templateSha256:$payload.template.sha256},
   regionalLaunches:($payload.regions | map_values({amiId:.workspaceImage.amiId,
     publisherAccountId:$payload.publisherAccountId,versionId:.templateLocation.versionId,
     templateSha256:.templateLocation.sha256,publicBoot:true,encryptedRoot:true,
     hostedTemplateValidated:true,cleanupVerified:true})),
   freshInstall:{region:"us-east-2",consoleIde:true,privateHttps:true,protectedSetup:true,anonymousPull:true,cliResume:true},
   gateResults:([range(1;15) | {key:("V"+tostring),value:"pass"}] | from_entries),cleanupVerified:true}' >"$qualification"
check_promotion() {
  "$promote_tool" verify "$manifest" "$1" "$release_assets" "$2" v1.4.2 "$source_commit" "$publisher"
}
expect_ok 'accept qualification of the exact RC payload for stable' check_promotion "$qualification" "$release_json"
for edit in '.candidate.sourceCommit = ("0" * 40)' '.candidate.releaseId = 2222' \
  '.candidate.assets["install.sh"].id = 2222' '.candidate.templateSha256 = ("0" * 64)' \
  'del(.regionalLaunches["us-west-2"])' '.regionalLaunches["us-east-2"].versionId = "different"' \
  '.regionalLaunches["us-east-2"].publisherAccountId = "222233334444"' \
  '.freshInstall.anonymousPull = false' '.freshInstall.consoleIde = false' \
  '.gateResults.V10 = "fail"' '.cleanupVerified = false'; do
  jq "$edit" "$qualification" >"$mutated"
  expect_rejected "reject incompatible qualification ($edit)" check_promotion "$mutated" "$release_json"
done
jq '.prerelease = false' "$release_json" >"$mutated"
expect_rejected 'reject a non-prerelease as RC evidence' check_promotion "$qualification" "$mutated"

# Reconcile a partial publication against a fixture REST API, then rerun it without another mutation.
fake_bin="$scratch/publication-bin"
mkdir "$fake_bin"
cat >"$fake_bin/git" <<'GIT'
#!/usr/bin/env bash
case "$1" in
  rev-parse) printf '%s\n' "$RELEASE_FIXTURE_SOURCE" ;;
  merge-base) exit 0 ;;
  *) exit 91 ;;
esac
GIT
cat >"$fake_bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
case "$1:$2" in
  api:--paginate) jq '[ [.] ]' "$RELEASE_FIXTURE_STATE" ;;
  release:upload)
    name=${4##*/}
    digest="sha256:$(shasum -a 256 "$4" | awk '{print $1}')"
    jq -e --arg name "$name" 'all(.assets[]; .name != $name)' "$RELEASE_FIXTURE_STATE" >/dev/null
    jq --arg name "$name" --arg digest "$digest" \
      '.assets += [{id:(1000 + (.assets|length)),name:$name,digest:$digest,state:"uploaded"}]' \
      "$RELEASE_FIXTURE_STATE" >"$RELEASE_FIXTURE_STATE.next"
    mv "$RELEASE_FIXTURE_STATE.next" "$RELEASE_FIXTURE_STATE"
    printf 'upload %s\n' "$name" >>"$RELEASE_FIXTURE_WRITES"
    ;;
  api:--method)
    if [ "$3" != PATCH ] || [ "$5" != -F ] || [ "$6" != draft=false ] || [ "$8" != make_latest=false ]; then exit 91; fi
    jq '.draft = false | .immutable = true' "$RELEASE_FIXTURE_STATE" >"$RELEASE_FIXTURE_STATE.next"
    mv "$RELEASE_FIXTURE_STATE.next" "$RELEASE_FIXTURE_STATE"
    printf 'publish\n' >>"$RELEASE_FIXTURE_WRITES"
    cat "$RELEASE_FIXTURE_STATE"
    ;;
  *) exit 91 ;;
esac
GH
chmod +x "$fake_bin/git" "$fake_bin/gh"
fixture_state="$scratch/draft-state.json"
fixture_writes="$scratch/draft-writes.log"
cp "$partial_json" "$fixture_state"
: >"$fixture_writes"
printf 'Fixture release notes\n' >"$scratch/notes.md"
publication_fixture() {
  env PATH="$fake_bin:$PATH" GITHUB_REPOSITORY=jflatten/ShiftBox GITHUB_EVENT_NAME=push GITHUB_REF_TYPE=tag \
    GITHUB_REF="refs/tags/$tag" GITHUB_REF_PROTECTED=true GITHUB_ACTOR=maintainer GITHUB_TRIGGERING_ACTOR=maintainer \
    GITHUB_SHA="$source_commit" GITHUB_RUN_ID=12345 SHIFTBOX_RELEASE_MAINTAINERS=maintainer SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID="$publisher" \
    RELEASE_FIXTURE_SOURCE="$source_commit" RELEASE_FIXTURE_STATE="$fixture_state" RELEASE_FIXTURE_WRITES="$fixture_writes" \
    "$publisher_tool" "$@"
}
# A frozen AMI handoff may resume only its own untouched build reservation.
body="<!-- shiftbox-candidate source=$source_commit run=12345 -->"
jq --arg body "$body" '.body=$body | .assets=[]' "$partial_json" >"$scratch/reservation.json"
cp "$scratch/reservation.json" "$fixture_state"
expect_ok 'resume the exact same-run draft reservation without writes' \
  publication_fixture resume-candidate "$tag" "$source_commit"
expect_rejected 'never restart a build against an existing reservation' \
  publication_fixture reserve-candidate "$tag" "$source_commit"
jq '.body |= sub("12345"; "45678")' "$scratch/reservation.json" >"$fixture_state"
expect_rejected 'reject a draft reservation from another run' \
  publication_fixture resume-candidate "$tag" "$source_commit"
cp "$partial_json" "$fixture_state"
expect_rejected 'reject a reservation with modified draft assets' \
  publication_fixture resume-candidate "$tag" "$source_commit"
expect_ok 'reservation validation leaves the draft unchanged' test ! -s "$fixture_writes"

expect_rejected 'refuse to expose the draft before missing assets are uploaded' \
  publication_fixture finalize "$tag" "$source_commit" "$release_assets"
expect_ok 'upload only the missing assets to the preserved draft' \
  publication_fixture prepare-draft "$tag" "$source_commit" "$release_assets" "$scratch/notes.md"
expect_ok 'publish the completed immutable candidate' \
  publication_fixture finalize "$tag" "$source_commit" "$release_assets"
expect_ok 'reuse the same published candidate on a retry' \
  publication_fixture prepare-draft "$tag" "$source_commit" "$release_assets" "$scratch/notes.md"
expect_ok 'make finalization of the published candidate a no-op' \
  publication_fixture finalize "$tag" "$source_commit" "$release_assets"
printf 'upload distribution-manifest.json\nupload shiftbox-aws-support.tar.gz\npublish\n' >"$scratch/expected-writes"
expect_ok 'preserve existing assets and publish exactly once' cmp "$scratch/expected-writes" "$fixture_writes"

printf '%s passed, %s failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
