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

# Subnet selection derives the VPC and storage AZ, without a routing-mode prompt.
(
    install_prompt() { IFS= read -r REPLY; }
    aws() {
        case "$*" in
            *'--output table') return 0 ;;
            *'ec2 describe-subnets --subnet-ids'*)
                jq -n --arg workspace_vpc "${WORKSPACE_VPC:-vpc-00000000000000001}" --argjson default_subnet "${DEFAULT_SUBNET:-false}" '
                  {Subnets:[{SubnetId:"subnet-00000000000000001",VpcId:"vpc-00000000000000001",
                    State:"available",AvailabilityZone:"us-east-2a",MapPublicIpOnLaunch:$default_subnet},
                    {SubnetId:"subnet-00000000000000002",VpcId:$workspace_vpc,
                    State:"available",AvailabilityZone:"us-east-2b",MapPublicIpOnLaunch:$default_subnet}]}' ;;
            *) return 97 ;;
        esac
    }
    printf '{}\n' >"$test_dir/subnet-state.json"
    answers=$'fresh\nsubnet-00000000000000001\nsubnet-00000000000000002\nfresh.internal\n\n'
    install_prompt_parameters "$test_dir/subnet-state.json" us-east-2 <<<"$answers" >/dev/null
    jq -e '.parameters | .VpcId == "vpc-00000000000000001" and .AvailabilityZone == "us-east-2a" and
      .ServiceSubnet == "subnet-00000000000000001" and .WorkspaceSubnet == "subnet-00000000000000002" and
      .OperatorCidr == "" and (has("AccessMode") or has("VpcCidr") or has("CreateNatGateway") or has("GitHubAppSecretArn") | not)' \
      "$test_dir/subnet-state.json" >/dev/null
    # A workspace subnet in a different VPC must fail before persisting inputs.
    WORKSPACE_VPC=vpc-00000000000000002 reject install_prompt_parameters "$test_dir/subnet-state.json" us-east-2 <<<"$answers"
    # One-pass launches override auto-assignment, so a default subnet is a valid input.
    answers=$'fresh\nsubnet-00000000000000001\nsubnet-00000000000000002\nfresh.internal\n172.31.0.1/32\n'
    DEFAULT_SUBNET=true install_prompt_parameters "$test_dir/subnet-state.json" us-east-2 one-pass <<<"$answers" >/dev/null
    jq -e '.parameters | .OperatorCidr == "172.31.0.1/32" and (has("RoleBoundaryMode") or has("GitHubAppSecretArn") | not)' \
      "$test_dir/subnet-state.json" >/dev/null
)

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
      {Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",Capabilities:["CAPABILITY_NAMED_IAM"],
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

# A change set with named-role acknowledgement can register a replacement task revision.
jq -n '{Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",Capabilities:["CAPABILITY_NAMED_IAM"],Changes:[{
  Type:"Resource",ResourceChange:{Action:"Modify",Replacement:"True",
  LogicalResourceId:"SetupTask",ResourceType:"AWS::ECS::TaskDefinition"}}]}' > "$test_dir/change.json"
printf '{"OperationEvents":[]}\n' > "$test_dir/events.json"
install_preview "$test_dir/change.json" "$test_dir/events.json" active >/dev/null

# The DNS updates accept only their two expected additions and reject a skipped phase,
# a live resource modification, and an old generic IAM capability.
jq -n '
  {Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",Capabilities:["CAPABILITY_NAMED_IAM"],
   Changes:({PrivateZone:"AWS::Route53::HostedZone",DiscoveryNamespace:"AWS::ServiceDiscovery::PrivateDnsNamespace"} |
     to_entries | map({Type:"Resource",ResourceChange:{Action:"Add",LogicalResourceId:.key,ResourceType:.value}}))}
' >"$test_dir/zones.json"
install_preview "$test_dir/zones.json" "$test_dir/events.json" dns-zones >/dev/null
reject install_preview "$test_dir/zones.json" "$test_dir/events.json" dns-services
reject install_preview "$test_dir/zones.json" "$test_dir/events.json" foundation
jq '.Changes[0].ResourceChange.Action = "Modify" | .Changes[0].ResourceChange.Replacement = "False"' \
    <"$test_dir/zones.json" >"$test_dir/changed-zones.json"
reject install_preview "$test_dir/changed-zones.json" "$test_dir/events.json" dns-zones
jq '.Capabilities = ["CAPABILITY_IAM"]' <"$test_dir/zones.json" >"$test_dir/wrong-capability.json"
reject install_preview "$test_dir/wrong-capability.json" "$test_dir/events.json" dns-zones
jq '
  .Changes = ({PostgresDiscovery:"AWS::ServiceDiscovery::Service",ControllerDiscovery:"AWS::ServiceDiscovery::Service"} |
    to_entries | map({Type:"Resource",ResourceChange:{Action:"Add",LogicalResourceId:.key,ResourceType:.value}}))
' <"$test_dir/zones.json" >"$test_dir/services.json"
install_preview "$test_dir/services.json" "$test_dir/events.json" dns-services >/dev/null
reject install_preview "$test_dir/services.json" "$test_dir/events.json" database
jq '.Changes = [{Type:"Resource", ResourceChange:{
  Action:"Add",LogicalResourceId:"PrivateRecords",ResourceType:"AWS::Route53::RecordSetGroup"}}]' \
    <"$test_dir/zones.json" >"$test_dir/records.json"
install_preview "$test_dir/records.json" "$test_dir/events.json" database >/dev/null
reject install_preview "$test_dir/records.json" "$test_dir/events.json" active

# A resumed DNS stage records the reviewed outputs exactly once and refuses a wrong
# phase, missing or duplicate outputs, foreign stack, changed DNS IDs, and changed storage.
stage_state="$test_dir/stage-state.json"
stage_stack="$test_dir/stage-stack.json"
jq -n --arg stack "$stack" '{stack:$stack}' >"$stage_state"
jq -n --arg stack "$stack" '
  {Stacks:[{StackId:$stack,StackStatus:"UPDATE_COMPLETE",
    Parameters:[{ParameterKey:"RoleBoundaryMode",ParameterValue:"managed"}],
    Outputs:({Phase:"dns-zones",VpcId:"vpc-00000000000000001",InstallationVolumeId:"vol-00000000000000001",
          ServiceSubnetId:"subnet-00000000000000001",WorkspaceSubnetId:"subnet-00000000000000002",
          PrivateZoneId:"Z123456789",DiscoveryHostedZoneId:"Z987654321",DiscoveryNamespaceId:"ns-0123456789abcdef"} |
      to_entries | map({OutputKey:.key,OutputValue:.value}))}]}
' >"$stage_stack"
install_store "$stage_state" '.parameters = {VpcId:"vpc-00000000000000001",
  ServiceSubnet:"subnet-00000000000000001",WorkspaceSubnet:"subnet-00000000000000002"}'
test "$(install_record_stage "$stage_state" "$stage_stack" dns-zones)" = dns-zones
test "$(install_record_stage "$stage_state" "$stage_stack")" = dns-zones
jq -e '.dnsIdentity.PrivateZoneId == "Z123456789" and .volume == "vol-00000000000000001" and .vpc == "vpc-00000000000000001"' \
    <"$stage_state" >/dev/null
reject install_record_stage "$stage_state" "$stage_stack" dns-services
for stage_edit in \
    '.Stacks[0].StackStatus = "UPDATE_IN_PROGRESS"' \
    '.Stacks[0].Parameters[0].ParameterValue = "external"' \
    '.Stacks[0].StackId += "foreign"' \
    '.Stacks[0].Outputs += [ .Stacks[0].Outputs[0] ]' \
    '.Stacks[0].Outputs |= map(select(.OutputKey != "DiscoveryNamespaceId"))' \
    '(.Stacks[0].Outputs[] | select(.OutputKey == "PrivateZoneId") | .OutputValue) = "Z999999999"' \
    '(.Stacks[0].Outputs[] | select(.OutputKey == "VpcId") | .OutputValue) = "vpc-00000000000000002"' \
    '(.Stacks[0].Outputs[] | select(.OutputKey == "WorkspaceSubnetId") | .OutputValue) = "subnet-00000000000000003"' \
    '(.Stacks[0].Outputs[] | select(.OutputKey == "InstallationVolumeId") | .OutputValue) = "vol-00000000000000002"'; do
    jq "$stage_edit" <"$stage_stack" >"$test_dir/edited-stage.json"
    reject install_record_stage "$stage_state" "$test_dir/edited-stage.json"
done
jq '
  (.Stacks[0].Outputs[] | select(.OutputKey == "Phase") | .OutputValue) = "dns-services" |
  .Stacks[0].Outputs += [
    {OutputKey:"PostgresDiscoveryServiceId",OutputValue:"srv-0123456789abcdef"},
    {OutputKey:"ControllerDiscoveryServiceId",OutputValue:"srv-fedcba9876543210"}]
' <"$stage_stack" >"$test_dir/services-stage.json"
test "$(install_record_stage "$stage_state" "$test_dir/services-stage.json" dns-services)" = dns-services
reject install_record_stage "$stage_state" "$stage_stack"

# An approved update must show its review and return only the phase so the installer
# continues to the next stage during the same invocation.
(
    install_change() {
        printf 'Review fixture change set for %s\n' "$2"
    }
    install_describe_stack() {
        cp "$test_dir/services-stage.json" "$3"
    }
    observed_phase=$(install_advance "$stage_state" dns-services us-east-2 "$stack" \
        "$stage_stack" "$test_dir" 2>"$test_dir/stage-review.txt")
    test "$observed_phase" = dns-services
    grep -Fxq 'Review fixture change set for dns-services' "$test_dir/stage-review.txt"
)

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

# A DNS change set is submitted only after explicit named-IAM acknowledgement and
# executed only after its original template and completed preview are approved.
(
    # shellcheck source=platform/release/manifest.sh
    source "$root/platform/release/manifest.sh"
    transaction="$test_dir/transaction"
    mkdir "$transaction"
    printf 'Resources: {}\n' >"$transaction/template.yaml"
    jq --arg stack "$stack" '
      .stack = $stack | .parameters = {DeploymentName:"fresh",RoleBoundaryMode:"managed"} |
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
                requested_name="shiftbox-dns-zones-${preview_digest:0:24}"
                description="ShiftBox reviewed dns-zones parameters sha256:$preview_digest"
                jq -n --arg name "$requested_name" --arg stack "$stack" --arg description "$description" \
                    --arg changeset "arn:aws:cloudformation:us-east-2:111111111111:changeSet/$requested_name/preview" \
                    --slurpfile parameters "$transaction/parameters.json" --slurpfile preview "$test_dir/zones.json" '
                    {Status:"CREATE_COMPLETE",ExecutionStatus:"AVAILABLE",
                     Description:$description,ChangeSetName:$name,ChangeSetId:$changeset,StackId:$stack,
                     Parameters:$parameters[0],Capabilities:$preview[0].Capabilities,Changes:$preview[0].Changes}'
                ;;
            cloudformation:wait) ;;
            cloudformation:get-template)
                if [ "$mismatch" = yes ]; then printf '{"TemplateBody":"Resources: foreign"}\n'
                else jq -n --rawfile contents "$transaction/template.yaml" '{TemplateBody:$contents}'
                fi
                ;;
            cloudformation:describe-events) printf '{"OperationEvents":[]}\n' ;;
            cloudformation:create-change-set)
                printf '%s\n' "$*" >>"$transaction/creation"
                missing_preview=false
                jq -n --arg stack "$stack" '{StackId:$stack}'
                ;;
            cloudformation:execute-change-set) printf '%s\n' "$*" >>"$transaction/execution" ;;
            *) printf 'Unexpected AWS call %s\n' "$*" >&2; return 97 ;;
        esac
    }
    # Invoked by install_change through the sourced installer.
    # shellcheck disable=SC2329
    install_prompt() {
        case "$1" in
            'Type CAPABILITY_NAMED_IAM to acknowledge this change set:') REPLY=$iam_reply ;;
            *) REPLY=$operator_reply ;;
        esac
    }

    missing_preview=true mismatch=no operator_reply=decline iam_reply=CAPABILITY_IAM
    reject install_change "$transaction/state.json" dns-zones UPDATE "$transaction"
    test ! -e "$transaction/creation"
    iam_reply=CAPABILITY_NAMED_IAM
    reject install_change "$transaction/state.json" dns-zones UPDATE "$transaction"
    grep -q -- '--capabilities CAPABILITY_NAMED_IAM' "$transaction/creation"
    test ! -e "$transaction/execution"
    missing_preview=false
    mismatch=yes operator_reply='EXECUTE dns-zones'
    reject install_change "$transaction/state.json" dns-zones UPDATE "$transaction"
    test ! -e "$transaction/execution"
    mismatch=no
    install_change "$transaction/state.json" dns-zones UPDATE "$transaction" >/dev/null
    test "$(wc -l <"$transaction/creation" | tr -d ' ')" = 1
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

# The one-pass preview allows a new bounded stack with both services and the
# initial claim. It rejects an update or a stack that tries to own the network.
jq -n '{
  Status:"CREATE_COMPLETE", ExecutionStatus:"AVAILABLE", Capabilities:["CAPABILITY_NAMED_IAM"],
  Changes: ([
    ["PostgresService", "AWS::ECS::Service"],
    ["ControllerService", "AWS::ECS::Service"],
    ["SetupClaim", "AWS::SecretsManager::Secret"],
    ["BrowserTrust", "AWS::SecretsManager::Secret"],
    ["InstallationVolume", "AWS::EC2::Volume"]
  ] | map({Type:"Resource",ResourceChange:{Action:"Add",LogicalResourceId:.[0],ResourceType:.[1]}}))
}' > "$test_dir/one-pass.json"
printf '{"OperationEvents":[]}\n' > "$test_dir/events.json"
install_preview "$test_dir/one-pass.json" "$test_dir/events.json" one-pass >/dev/null
jq '.Changes[0].ResourceChange.Action = "Modify" | .Changes[0].ResourceChange.Replacement = "False"' \
    "$test_dir/one-pass.json" > "$test_dir/modified-one-pass.json"
reject install_preview "$test_dir/modified-one-pass.json" "$test_dir/events.json" one-pass
jq '.Changes += [{Type:"Resource",ResourceChange:{Action:"Add",LogicalResourceId:"Network",ResourceType:"AWS::EC2::VPC"}}]' \
    "$test_dir/one-pass.json" > "$test_dir/network-one-pass.json"
reject install_preview "$test_dir/network-one-pass.json" "$test_dir/events.json" one-pass
