#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
installer="$root/scripts/install-aws.sh"
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
export AWS_TEST_TRACE="$test_dir/aws-trace"
mkdir "$test_dir/bin"
cat > "$test_dir/bin/aws" <<'AWS'
#!/bin/sh
printf '%s\n' "$*" >> "$AWS_TEST_TRACE"
exit 97
AWS
chmod +x "$test_dir/bin/aws"
export PATH="$test_dir/bin:$PATH"
: > "$AWS_TEST_TRACE"

# The help path is usable without AWS credentials or an interactive terminal.
bash "$installer" --help > "$test_dir/help" </dev/null
grep -Eq -- '--version|--help' "$test_dir/help"
cat "$installer" | bash -s -- --help > "$test_dir/piped-help"
cmp "$test_dir/help" "$test_dir/piped-help"
test ! -s "$AWS_TEST_TRACE"

# A truncated pipe and sourcing the wrapper cannot provision or read AWS resources.
head -c 150 "$installer" | bash -s -- > "$test_dir/truncated" 2>&1 || true
test ! -s "$AWS_TEST_TRACE"
# shellcheck disable=SC1090
source "$installer"
test ! -s "$AWS_TEST_TRACE"

reject() {
    if "$@" > "$test_dir/rejected.out" 2>&1; then
        printf 'Expected %s to reject the fixture.\n' "$1" >&2
        exit 1
    fi
}

release_fixture() {
    jq -n --arg tag "$1" --argjson prerelease "$2" --argjson draft "$3" '
    {id:101,tag_name:$tag,immutable:true,prerelease:$prerelease,draft:$draft,published_at:"2026-01-02T03:04:05Z",
     html_url:("https://github.com/jflatten/ShiftBox/releases/tag/"+$tag),assets:[
        {id:102,name:"install.sh"},{id:103,name:"distribution-manifest.json"},
        {id:104,name:"shiftbox-aws-cloudformation.yaml"},{id:105,name:"shiftbox-aws-support.tar.gz"}
     ]}' > "$test_dir/release.json"
}

# Default selection accepts a published stable while a pinned RC requires exact agreement.
release_fixture v1.2.3 false false
test "$(install_release '' "$test_dir/release.json")" = v1.2.3
test "$(install_release v1.2.3 "$test_dir/release.json")" = v1.2.3
release_fixture v1.2.3-rc.4 true false
test "$(install_release v1.2.3-rc.4 "$test_dir/release.json")" = v1.2.3-rc.4
reject install_release '' "$test_dir/release.json"
reject install_release v1.2.3-rc.5 "$test_dir/release.json"

# A draft, incomplete release or absent stable cannot select a distributable payload.
release_fixture v1.2.3 false true
reject install_release v1.2.3 "$test_dir/release.json"
printf '{"message":"Not Found"}\n' > "$test_dir/release.json"
reject install_release '' "$test_dir/release.json"

preview_fixture() {
    jq -n --arg action "$1" --arg replacement "$2" '
      {Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",
       Changes:[{Type:"Resource",ResourceChange:{Action:$action,Replacement:$replacement,
             LogicalResourceId:"InstallationVolume",ResourceType:"AWS::EC2::Volume"}}]}' > "$test_dir/change.json"
}

# A reviewed successful preview can continue when it preserves installation storage.
preview_fixture Modify False
printf '{"OperationEvents":[]}\n' > "$test_dir/events.json"
install_preview "$test_dir/change.json" "$test_dir/events.json" active > /dev/null

# Deletion, replacement, and failed service-side validation block execution.
preview_fixture Remove False
reject install_preview "$test_dir/change.json" "$test_dir/events.json" active
preview_fixture Modify True
reject install_preview "$test_dir/change.json" "$test_dir/events.json" active
preview_fixture Modify False
printf '{"OperationEvents":[{"EventType":"VALIDATION_ERROR","ValidationStatus":"FAILED","ValidationStatusReason":"fixture CIDR"}]}\n' > "$test_dir/events.json"
reject install_preview "$test_dir/change.json" "$test_dir/events.json" active

# A resumed installation is tied to the reviewed account, region, stack and release.
state="$test_dir/state.json"
stack='arn:aws:cloudformation:us-east-2:111111111111:stack/fresh/00000000-0000-0000-0000-000000000001'
jq -n --arg stack "$stack"     '{account:"111111111111",region:"us-east-2",stack:$stack,release:"v1.2.3"}' > "$state"
install_identity "$state" 111111111111 us-east-2 "$stack" v1.2.3
reject install_identity "$state" 222222222222 us-east-2 "$stack" v1.2.3
reject install_identity "$state" 111111111111 us-west-2 "$stack" v1.2.3
reject install_identity "$state" 111111111111 us-east-2 "${stack%1}2" v1.2.3
reject install_identity "$state" 111111111111 us-east-2 "$stack" v1.2.3-rc.4
test ! -s "$AWS_TEST_TRACE"

# New change sets reject unexpected workspace data replacement and allow a task revision.
jq -n '{Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",Changes:[{
  Type:"Resource",ResourceChange:{Action:"Modify",Replacement:"True",
  LogicalResourceId:"SetupTask",ResourceType:"AWS::ECS::TaskDefinition"}}]}' > "$test_dir/change.json"
printf '{"OperationEvents":[]}\n' > "$test_dir/events.json"
install_preview "$test_dir/change.json" "$test_dir/events.json" database >/dev/null

# A deployment fingerprint binds a concrete stack name and immutable payload.
state="$test_dir/started.json"
printf '{"account":"111111111111","region":"us-east-2","stackName":"fresh","stack":"","release":"v1.2.3","manifestSha256":"fixture"}\n' > "$state"
install_bind_stack "$state" "$stack"
install_identity "$state" 111111111111 us-east-2 "$stack" v1.2.3
reject install_bind_stack "$state" "${stack%1}2"
reject install_bind_stack "$state" "${stack/111111111111/222222222222}"
test ! -s "$AWS_TEST_TRACE"

# An invocation with no controlling terminal stops before reading AWS credentials.
python3 - "$installer" <<'PY'
import subprocess
import sys

result = subprocess.run(["bash", sys.argv[1]], start_new_session=True, capture_output=True, text=True)
assert result.returncode != 0 and "interactive terminal is required" in result.stderr, result
PY
test ! -s "$AWS_TEST_TRACE"

# A proposed change set is submitted only after explicit IAM acknowledgement and
# executed only after its original template and completed preview are approved.
(
    # shellcheck source=platform/release/manifest.sh
    source "$root/platform/release/manifest.sh"
    transaction="$test_dir/transaction"
    mkdir "$transaction"
    printf 'Resources: {}\n' >"$transaction/template.yaml"
    jq --arg stack "$stack" '
      .stack = $stack | .parameters = {DeploymentName:"fresh"} |
      .templateUrl = "https://fixture.invalid/versioned"
    ' <"$state" >"$transaction/state.json"

    # Invoked by install_change through the sourced installer.
    # shellcheck disable=SC2329
    aws() {
        local requested_name description preview_digest
        case "$3:$4" in
            sts:get-caller-identity)
                printf '111111111111\n'
                ;;
            cloudformation:describe-change-set)
                if [ "${missing_preview:-false}" = true ]; then
                    printf 'ChangeSetNotFound: fixture review does not exist\n' >&2
                    return 1
                fi
                preview_digest=$({ cat "$transaction/parameters.json"; jq -r .manifestSha256 <"$transaction/state.json"; } |
                    openssl dgst -sha256 -r)
                preview_digest=${preview_digest%% *}
                requested_name="shiftbox-database-${preview_digest:0:24}"
                description="ShiftBox reviewed database parameters sha256:$preview_digest"
                jq -n --arg name "$requested_name" --arg stack "$stack" --arg description "$description" \
                    --arg changeset "arn:aws:cloudformation:us-east-2:111111111111:changeSet/$requested_name/preview" \
                    --slurpfile parameters "$transaction/parameters.json" '
                    {Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",
                     Description:$description,ChangeSetName:$name,ChangeSetId:$changeset,StackId:$stack,
                     Parameters:$parameters[0],Changes:[]}'
                ;;
            cloudformation:wait) ;;
            cloudformation:get-template)
                if [ "$mismatch" = yes ]; then printf '{"TemplateBody":"Resources: foreign"}\n'
                else jq -n --rawfile contents "$transaction/template.yaml" '{TemplateBody:$contents}'
                fi
                ;;
            cloudformation:describe-events) printf '{"OperationEvents":[]}\n' ;;
            cloudformation:create-change-set) printf '%s\n' "$*" >>"$transaction/creation" ;;
            cloudformation:execute-change-set) printf '%s\n' "$*" >>"$transaction/execution" ;;
            *) printf 'Unexpected AWS call %s\n' "$*" >&2; return 97 ;;
        esac
    }
    # Invoked by install_change through the sourced installer.
    # shellcheck disable=SC2329
    install_prompt() {
        REPLY=$operator_reply
    }

    missing_preview=true mismatch=no operator_reply='decline'
    reject install_change "$transaction/state.json" database UPDATE "$transaction"
    test ! -e "$transaction/creation"
    missing_preview=false
    reject install_change "$transaction/state.json" database UPDATE "$transaction"
    test ! -e "$transaction/execution"
    mismatch=yes operator_reply='EXECUTE database'
    reject install_change "$transaction/state.json" database UPDATE "$transaction"
    test ! -e "$transaction/execution"
    mismatch=no operator_reply='EXECUTE database'
    install_change "$transaction/state.json" database UPDATE "$transaction" >/dev/null
    test "$(wc -l <"$transaction/execution" | tr -d ' ')" = 1
)

# Browser and database trust are independently issued and survive an interrupted run.
(
    umask 077
    install_certificate "$test_dir/db" 'Fixture DB CA' 'postgres.example.internal' 'DNS:postgres.example.internal'
    install_certificate "$test_dir/browser" 'Fixture Browser CA' 'shiftbox.example.internal' \
      'DNS:shiftbox.example.internal,DNS:*.ide.example.internal,DNS:*.preview.example.internal,DNS:*.repair.example.internal'
    openssl verify -CAfile "$test_dir/db/ca.pem" "$test_dir/db/leaf.pem" >/dev/null
    openssl verify -CAfile "$test_dir/browser/ca.pem" "$test_dir/browser/leaf.pem" >/dev/null
    reject openssl verify -CAfile "$test_dir/browser/ca.pem" "$test_dir/db/leaf.pem"
    before=$(openssl x509 -noout -sha256 -fingerprint -in "$test_dir/db/ca.pem")
    install_certificate "$test_dir/db" 'Fixture DB CA' 'postgres.example.internal' 'DNS:postgres.example.internal'
    test "$before" = "$(openssl x509 -noout -sha256 -fingerprint -in "$test_dir/db/ca.pem")"
)

# A region cannot reuse another region's AMI or accept an image owned by someone else.
(
    image_manifest="$test_dir/image-manifest.json"
    jq -n '{
      publisherAccountId:"111122223333",
      regions:{"us-east-2":{
        "workspaceImage":{amiId:"ami-00000000000000001",rootDeviceName:"/dev/sda1",rootSnapshotId:"snap-00000000000000001"},
        "hostAmiId":"ami-00000000000000002"
      }}
    }' >"$image_manifest"
    # Invoked by install_verify_aws_images through the sourced installer.
    # shellcheck disable=SC2329
    aws() {
        [ "$1" = --region ] && [ "$2" = us-east-2 ] && [ "$3" = ec2 ] &&
            [ "$4" = describe-images ] && [ "$5" = --owners ] || return 97
        case "$6" in
            111122223333)
                jq -n --arg owner "$observed_owner" '{
                    Images:[{ImageId:"ami-00000000000000001",OwnerId:$owner,
                    State:"available",Public:true,Architecture:"x86_64",
                    VirtualizationType:"hvm",RootDeviceName:"/dev/sda1",RootDeviceType:"ebs",
                    BlockDeviceMappings:[{DeviceName:"/dev/sda1",Ebs:{SnapshotId:"snap-00000000000000001",Encrypted:false}}]}]
                }'
                ;;
            amazon)
                printf '{"Images":[{"ImageId":"ami-00000000000000002","State":"available","Architecture":"x86_64"}]}\n'
                ;;
            *) return 97 ;;
        esac
    }
    observed_owner=111122223333
    install_verify_aws_images "$image_manifest" us-east-2
    observed_owner=444455556666
    reject install_verify_aws_images "$image_manifest" us-east-2
    reject install_verify_aws_images "$image_manifest" us-west-2
)
# Selecting a private link with an unrestricted source route cannot produce stack inputs.
(
    bad_route="$test_dir/bad-route.json"
    printf '{}\n' >"$bad_route"
    # Invoked by install_prompt_parameters through the sourced installer.
    # shellcheck disable=SC2329
    aws() {
        printf 'us-east-2a\n'
    }
    responses=(fixture us-east-2a 10.63.0.0/16 private-link operator.internal 0.0.0.0/0)
    response_index=0
    # Invoked by install_prompt_parameters through the sourced installer.
    # shellcheck disable=SC2329
    install_prompt() {
        REPLY=${responses[$response_index]}
        response_index=$((response_index + 1))
    }
    reject install_prompt_parameters "$bad_route" us-east-2
    test "$(cat "$bad_route")" = '{}'
)
# A failed preflight cleans its new work directory so a fresh retry can use it.
preflight="$test_dir/preflight"
mkdir -p "$preflight/.install-lock" "$preflight/release" "$preflight/run.fixture"
install_cleanup "$preflight" "$preflight/run.fixture" false
test ! -e "$preflight"

# A recoverable deployment keeps both its committed metadata and its verified assets.
resumable="$test_dir/resumable"
mkdir -p "$resumable/.install-lock" "$resumable/release" "$resumable/run.fixture"
printf '{}\n' >"$resumable/state.json"
install_cleanup "$resumable" "$resumable/run.fixture" false
test -f "$resumable/state.json"
test -d "$resumable/release"
test ! -e "$resumable/.install-lock"
printf 'Installer fixtures passed.\n'
