#!/usr/bin/env bash
# Review and create the private Ohio stack from a locally rendered one-pass artifact.
set -euo pipefail
umask 077
export AWS_PAGER='' AWS_CLI_AUTO_PROMPT=off AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=scripts/install-aws.sh
source "$here/../../scripts/install-aws.sh"

usage() {
    printf 'Usage: %s <rendered-template-directory> <new-work-directory> [--resume]\n' "$0"
}
if [[ ${1:-} == --help ]]; then
    usage
    exit 0
fi
if [[ $# -lt 2 || $# -gt 3 || ( $# == 3 && $3 != --resume ) ]]; then
    usage >&2
    exit 2
fi

aws() { command aws --profile shiftbox-testing "$@"; }
account=888293065132
region=us-east-2
release=development
artifact=$(cd -- "$1" && pwd -P)
template="$artifact/shiftbox-aws-development.json"
policy="$artifact/stack-policy.json"
work=$2
resume=false
[[ ${3:-} != --resume ]] || resume=true
if ! { exec 3<> /dev/tty; } 2>/dev/null; then
    install_fail 'an interactive terminal is required to review the change set'
    exit 1
fi
install_require_tools one-pass
install_current_account "$account" "$region"
jq -e --arg account "$account" --arg region "$region" '
    (.Rules.DevelopmentDestination.Assertions | map(.Assert."Fn::Equals")) as $guards |
    ($guards | index([{"Ref":"AWS::AccountId"},$account]) != null) and
    ($guards | index([{"Ref":"AWS::Region"},$region]) != null) and
    (.Parameters | has("Phase") | not) and
    .Resources.ControllerTask.Properties.ContainerDefinitions[0].Command == ["one-pass"] and
    (.Resources.ControllerTask.Properties.ContainerDefinitions[0].Image |
      test("^" + $account + "[.]dkr[.]ecr[.]" + $region + "[.]amazonaws[.]com/shiftbox-development@sha256:[0-9a-f]{64}$")) and
    .Outputs.SetupClaimSecretArn.Value.Ref == "SetupClaim" and .Outputs.BrowserCaSecretArn.Value.Ref == "BrowserTrust"
' "$template" >/dev/null
test -s "$policy"

if $resume; then
    [[ -d $work && ! -L $work && -O $work && -f $work/state.json && ! -L $work/state.json ]]
else
    mkdir -m 700 -- "$work"
fi
work=$(cd -- "$work" && pwd -P)
state="$work/state.json"
mkdir -- "$work/.install-lock" || { install_fail 'another process owns this work directory'; exit 1; }
scratch=''
trap 'if [ -n "$scratch" ]; then rm -rf -- "$scratch"; fi; rmdir -- "$work/.install-lock"' EXIT
scratch=$(mktemp -d "$work/run.XXXXXXXX")
jq -cj . "$template" > "$scratch/template.yaml"
if [ "$(wc -c <"$scratch/template.yaml")" -gt 51200 ]; then
    install_fail 'the template exceeds the CLI body limit. Upload the file in the CloudFormation console.'
    exit 1
fi
digest=$(openssl dgst -sha256 -r "$scratch/template.yaml")
digest=${digest%% *}
if $resume; then
    jq -e --arg sha "$digest" --arg account "$account" --arg region "$region" --arg release "$release" '
        .account == $account and .region == $region and .release == $release and .templateSha256 == $sha
    ' "$state" >/dev/null
else
    aws --region "$region" cloudformation validate-template --template-body "file://$scratch/template.yaml" >/dev/null
    jq -n --arg account "$account" --arg region "$region" --arg release "$release" --arg sha "$digest" '
        {account:$account,region:$region,release:$release,templateSha256:$sha,stack:""}
    ' > "$scratch/state.json"
    install_prompt_parameters "$scratch/state.json" "$region" one-pass
    mv -- "$scratch/state.json" "$state"
fi

install_establish_stack "$state" "$work" "$scratch" one-pass
stack=$(jq -er '.stack' "$state")
stack_file="$scratch/stack.json"
expected_origin=$(jq -er '"https://shiftbox." + .parameters.DnsSuffix + ":8443"' "$state")
origin=$(install_output "$stack_file" ManagementOrigin)
[[ $origin == "$expected_origin" ]]
claim=$(install_output "$stack_file" SetupClaimSecretArn)
trust=$(install_output "$stack_file" BrowserCaSecretArn)
[[ $claim == "arn:aws:secretsmanager:$region:$account:secret:"* && $trust == "arn:aws:secretsmanager:$region:$account:secret:"* && $claim != "$trust" ]]
install_protect_stack "$account" "$region" "$stack" "$stack_file" "$policy"
printf 'Private ShiftBox URL: %s\nBrowser trust CA ARN: %s\nSetup claim ARN: %s\n' "$origin" "$trust" "$claim"
printf 'Trust the public CA from the stack, then open the URL and complete the wizard.\n'
