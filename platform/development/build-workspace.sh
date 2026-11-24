#!/usr/bin/env bash
# Keep the existing private Packer recipe on the testing account and a supplied network.
set -euo pipefail
umask 077
here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
if [[ $# != 3 || ! $1 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.[1-9][0-9]*$ || $3 != *.pkrvars.json ]]; then
  printf 'Usage: %s <existing guest compatibility version> <guest-update.pub> <testing.pkrvars.json>\n' "$0" >&2
  exit 2
fi
region=us-east-2
account=888293065132
export AWS_PROFILE=shiftbox-testing AWS_REGION="$region" AWS_DEFAULT_REGION="$region"
export AWS_PAGER='' AWS_CLI_AUTO_PROMPT=off AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true
unset AWS_DEFAULT_PROFILE AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_SECURITY_TOKEN
[[ $(aws --profile "$AWS_PROFILE" --region "$region" sts get-caller-identity --query Account --output text) == "$account" ]] || exit 1
jq -e --arg region "$region" '.region == $region and (.source_ami | test("^ami-[0-9a-f]{17}$")) and
  (.build_subnet_id | test("^subnet-[0-9a-f]{17}$")) and
  (.build_security_group_id | test("^sg-[0-9a-f]{17}$")) and
  .cluster_name == "shiftbox-development" and (.publisher_attempt // "") == ""' "$3" >/dev/null
subnet=$(jq -er .build_subnet_id "$3")
vpc=$(aws ec2 describe-subnets --subnet-ids "$subnet" --output json |
  jq -er --arg account "$account" '.Subnets | select(length == 1) | .[0] |
    select(.OwnerId == $account and .MapPublicIpOnLaunch == false and .State == "available") | .VpcId')
group=$(jq -er .build_security_group_id "$3")
aws ec2 describe-security-groups --group-ids "$group" --output json |
  jq -e --arg vpc "$vpc" --arg account "$account" '.SecurityGroups | length == 1 and
    (.[0].VpcId == $vpc and .[0].OwnerId == $account)' >/dev/null
revision=$(git -C "$here" rev-parse --short=12 HEAD)
name="dev-$(date -u +%Y%m%d%H%M%S)-$revision"
RELEASE_CANDIDATE='' PUBLIC_AMI_BUILD=0 GUEST_AGENT_RELEASE="$1" \
  "$here/../packer/aws/build-image.sh" "$name" "$2" "$3"
