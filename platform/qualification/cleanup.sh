#!/usr/bin/env bash
set -euo pipefail

# Keep private account IDs in protected workflow variables.
readonly account=${SHIFTBOX_QUALIFICATION_ACCOUNT_ID:-}
readonly publisher=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
readonly demo=524941137496 repo=jflatten/ShiftBox workflow=.github/workflows/qualify-aws.yml
readonly run_pattern='^sbq-[1-9][0-9]{0,15}-[1-9][0-9]{0,3}$'
readonly scope_pattern='^sbq-[1-9][0-9]{0,15}-[1-9][0-9]{0,3}(-p)?$'
readonly regions=(us-east-1 us-east-2 us-west-1 us-west-2)
export AWS_MAX_ATTEMPTS=2 AWS_PAGER=
deadline=$((SECONDS + 720))

fail() { printf 'ShiftBox qualification cleanup: %s\n' "$*" >&2; return 1; }

aws_read() {
  local region=$1 response
  shift
  # Public workflow logs must not expose private resource identifiers from
  # AWS API errors. Operators can inspect those errors with scoped access.
  response=$(aws --region "$region" "$@" --output json --cli-connect-timeout 8 --cli-read-timeout 20 2>/dev/null) ||
    { fail "AWS $1 $2 failed in $region"; return 1; }
  printf '%s\n' "$response"
}

assert_account() {
  local actual
  [[ $account =~ ^[0-9]{12}$ && $publisher =~ ^[0-9]{12}$ &&
    $account != "$publisher" && $account != "$demo" && $publisher != "$demo" ]] ||
    { fail 'missing or unsafe dedicated testing account configuration'; return 1; }
  actual=$(aws --region us-east-2 sts get-caller-identity --query Account --output text \
    --cli-connect-timeout 8 --cli-read-timeout 20 2>/dev/null) ||
    { fail 'could not verify the AWS caller'; return 1; }
  [[ $actual == "$account" ]] || fail 'refusing to operate outside the dedicated testing account'
}

# Use authoritative service state for stacks and tagged inventory for detached
# artifacts. Tags in every inventory row are checked again before acting.
tagged() {
  local region=$1 name=${2:-} cluster_filter="Key=cluster_name"
  if [[ -n $name ]]; then cluster_filter+=",Values=$name"; fi
  aws_read "$region" resourcegroupstaggingapi get-resources \
    --tag-filters Key=project,Values=shiftbox "$cluster_filter" |
    jq -ec --arg name "$name" '
      def tag($key): [.Tags[] | select(.Key == $key) | .Value];
      .ResourceTagMappingList | if type != "array" then error("invalid tagged inventory") else . end |
      if all(.[]; (.ResourceARN | type == "string") and tag("project") == ["shiftbox"] and
        (tag("cluster_name") | length == 1 and (if $name == "" then true else .[0] == $name end)))
      then . else error("returned inventory does not match ownership filter") end'
}

stacks() {
  local region=$1
  aws_read "$region" cloudformation list-stacks |
    jq -ec '.StackSummaries | if type != "array" then error("invalid stack inventory")
      else map(select(.StackStatus != "DELETE_COMPLETE")) end'
}

discover() {
  local region inventory summary
  for region in "${regions[@]}"; do
    inventory=$(tagged "$region") || return
    summary=$(stacks "$region") || return
    jq -nr --argjson tagged "$inventory" --argjson stacks "$summary" --arg pattern "$scope_pattern" '
      ($stacks[].StackName, ($tagged[].Tags[] | select(.Key == "cluster_name") | .Value)) |
      select(type == "string" and test($pattern)) | sub("-p$"; "")' || return
  done
}

# Require the real workflow and branch, and check the latest run as well as the
# named attempt. A missing or unverifiable run is never reaped automatically.
lookup_run() {
  local id=$1 attempt=${2:-} endpoint="repos/$repo/actions/runs/$1" response
  if [[ -n $attempt ]]; then endpoint+="/attempts/$attempt"; fi
  response=$(gh api "$endpoint") || { fail "could not look up qualification run $id"; return 1; }
  jq -ec --arg id "$id" --arg attempt "$attempt" --arg path "$workflow" --arg repo "$repo" '
    select((.id | tostring) == $id and (.path == $path or .path == ($path + "@main") or
      .path == ($path + "@refs/heads/main")) and .event == "workflow_dispatch" and
      .head_branch == "main" and .head_repository.full_name == $repo and .actor.login == "jflatten" and
      (.status | type == "string") and
      ($attempt == "" or ((.run_attempt | tostring) == $attempt and .triggering_actor.login == "jflatten")))' <<<"$response" ||
    fail "could not verify qualification run identity $id"
}

owned_stack() {
  local region=$1 name=$2 id=$3 body
  local prefix="arn:aws:cloudformation:$region:$account:stack/$name/"
  if [[ $id != "$prefix"* || ! ${id#"$prefix"} =~ ^[a-f0-9-]+$ ]]; then
    fail "unverified stack ARN for $name in $region"
    return 1
  fi
  body=$(aws_read "$region" cloudformation describe-stacks --stack-name "$id") || return
  jq -ec --arg id "$id" --arg name "$name" '
    def tag($key): [.Tags[] | select(.Key == $key) | .Value];
    .Stacks | select(length == 1) | .[0] |
    select(.StackId == $id and .StackName == $name and tag("project") == ["shiftbox"] and
      tag("cluster_name") == [$name] and tag("shiftbox_qualification_run") == [$name])' <<<"$body" ||
    fail "could not verify ownership of qualification stack $name in $region"
}

stack_resources() {
  local region=$1 id=$2
  aws_read "$region" cloudformation list-stack-resources --stack-name "$id" |
    jq -ec '.StackResourceSummaries | if type != "array" then error("invalid stack resource inventory") else . end'
}

# CloudFormation owns regular dependencies such as NAT, Route 53, Cloud Map
# and the ECS host. Check its resource-level deletion results before the
# independent run reaps retained data and checks the remaining tag inventory.
verify_stack_deleted() {
  local region=$1 name=$2 id=$3 resources
  resources=$(stack_resources "$region" "$id") || return
  jq -e '
    all(.[];
      if (.PhysicalResourceId // "") == "" or .ResourceType == "AWS::SecretsManager::Secret" or
        (.ResourceType == "AWS::EC2::Volume" and .LogicalResourceId == "InstallationVolume")
      then true else .ResourceStatus == "DELETE_COMPLETE" end)' <<<"$resources" >/dev/null ||
    fail "could not verify regular AWS stack resource deletion for $name in $region"
}

# Before the CloudFormation delete, drain only the out-of-stack workspaces
# and the staffed disposable restore target. CFN owns the ECS host and router.
live_instances() {
  local region=$1 name=$2 workspace_only=$3
  local -a filters=("Name=tag:project,Values=shiftbox" "Name=tag:cluster_name,Values=$name"
    "Name=instance-state-name,Values=pending,running,shutting-down,stopping,stopped")
  aws_read "$region" ec2 describe-instances --filters "${filters[@]}" |
    jq -ec --arg account "$account" --arg name "$name" --argjson workspace "$workspace_only" '
      def tag($key): [.Tags[] | select(.Key == $key) | .Value];
      if (.Reservations | type) != "array" then error("invalid EC2 inventory") else .Reservations end |
      [ .[] | if .OwnerId != $account then error("incorrect EC2 owner") else .Instances[] end |
        if tag("project") == ["shiftbox"] and tag("cluster_name") == [$name] and
          (.InstanceId | test("^i-[a-f0-9]+$")) and (.VpcId | test("^vpc-[a-f0-9]+$"))
        then select(($workspace | not) or tag("ShiftBoxWorkspaceDeployment") == [$name] or
          tag("shiftbox_qualification_component") == ["restore"])
        else error("incorrect EC2 ownership") end ]'
}

verify_nat_deleted() {
  local region=$1 name=$2 response
  response=$(aws_read "$region" ec2 describe-nat-gateways --filter \
    Name=tag:project,Values=shiftbox "Name=tag:cluster_name,Values=$name") || return
  jq -e '(.NatGateways | type == "array") and all(.NatGateways[]; .State == "deleted")' \
    <<<"$response" >/dev/null || fail "a tagged NAT gateway remains for $name in $region"
}

assert_vpc() {
  local region=$1 name=$2 vpc=$3 response
  [[ $vpc =~ ^vpc-[a-f0-9]+$ ]] || return 1
  response=$(aws_read "$region" ec2 describe-vpcs --vpc-ids "$vpc") || return
  jq -e --arg name "$name" --arg id "$vpc" --arg account "$account" '
    def tag($key): [.Tags[] | select(.Key == $key) | .Value];
    .Vpcs | length == 1 and (.[0] | .VpcId == $id and .OwnerId == $account and
      .IsDefault == false and tag("project") == ["shiftbox"] and tag("cluster_name") == [$name])' \
    <<<"$response" >/dev/null || fail "refusing to touch an unverified or default VPC for $name in $region"
}

# Re-inspect live IDs before deletion. EC2 network resources must also belong
# to this exact nondefault VPC. Attached resources are left for a later pass.
delete_ec2_orphan() {
  local region=$1 name=$2 kind=$3 id=$4 response item vpc
  local describe filter array identity action option id_pattern
  case "$kind" in
    volume)
      describe='describe-volumes'; filter=volume-ids; array=Volumes; identity=VolumeId
      action=delete-volume; option=volume-id; id_pattern=vol ;;
    elastic-ip)
      describe='describe-addresses'; filter=allocation-ids; array=Addresses; identity=AllocationId
      action=release-address; option=allocation-id; id_pattern=eipalloc ;;
    network-interface)
      describe='describe-network-interfaces'; filter=network-interface-ids; array=NetworkInterfaces; identity=NetworkInterfaceId
      action=delete-network-interface; option=network-interface-id; id_pattern=eni ;;
    subnet)
      describe='describe-subnets'; filter=subnet-ids; array=Subnets; identity=SubnetId
      action=delete-subnet; option=subnet-id; id_pattern=subnet ;;
    security-group)
      describe='describe-security-groups'; filter=group-ids; array=SecurityGroups; identity=GroupId
      action=delete-security-group; option=group-id; id_pattern=sg ;;
    vpc) assert_vpc "$region" "$name" "$id" || return
      aws_read "$region" ec2 delete-vpc --vpc-id "$id" >/dev/null
      return ;;
    *) fail "unsupported orphaned EC2 resource type $kind"; return 1 ;;
  esac
  [[ $id =~ ^${id_pattern}-[a-f0-9]+$ ]] || { fail "invalid EC2 $kind identifier"; return 1; }
  response=$(aws_read "$region" ec2 "$describe" "--$filter" "$id") || return
  item=$(jq -ec --arg name "$name" --arg id "$id" --arg key "$identity" --arg array "$array" '
    def tag($key): [.Tags[] | select(.Key == $key) | .Value];
    .[$array] | select(length == 1) | .[0] |
    select(.[$key] == $id and tag("project") == ["shiftbox"] and tag("cluster_name") == [$name])' <<<"$response") ||
    { fail "could not reverify orphaned $kind for $name in $region"; return 1; }
  case "$kind" in
    volume) jq -e '.State == "available" and .Attachments == []' <<<"$item" >/dev/null || return 0 ;;
    elastic-ip) jq -e '.Domain == "vpc" and .AssociationId == null and .NetworkInterfaceId == null' <<<"$item" >/dev/null || return 0 ;;
    network-interface) jq -e '.Status == "available" and .Attachment == null and .RequesterManaged == false' <<<"$item" >/dev/null || return 0 ;;
    security-group) jq -e '.GroupName != "default"' <<<"$item" >/dev/null || { fail 'refusing to touch a default security group'; return 1; } ;;
    subnet) jq -e '.DefaultForAz == false' <<<"$item" >/dev/null || { fail 'refusing to touch a default subnet'; return 1; } ;;
  esac
  case "$kind" in
    network-interface|subnet|security-group)
      vpc=$(jq -er '.VpcId' <<<"$item") || return
      assert_vpc "$region" "$name" "$vpc" || return ;;
  esac
  aws_read "$region" ec2 "$action" "--$option" "$id" >/dev/null
}

delete_retained_secret() {
  local region=$1 name=$2 arn=$3 response prefix="arn:aws:secretsmanager:$1:$account:secret:$2-Postgres"
  # The approved stack generates these four physical names. Never touch a
  # supplied GitHub App secret or another credential tagged by an operator.
  if [[ $arn != "$prefix"* || ! ${arn#"$prefix"} =~ ^(Admin|App|Dex|Tls)Secret-[A-Za-z0-9-]+$ ]]; then
    fail "unrecognized retained secret for $name in $region"
    return 1
  fi
  response=$(aws_read "$region" secretsmanager describe-secret --secret-id "$arn") || return
  jq -e --arg arn "$arn" --arg name "$name" '
    def tag($key): [.Tags[] | select(.Key == $key) | .Value];
    .ARN == $arn and tag("project") == ["shiftbox"] and tag("cluster_name") == [$name]' \
    <<<"$response" >/dev/null || { fail "unverified retained secret for $name in $region"; return 1; }
  if jq -e '.DeletedDate == null' <<<"$response" >/dev/null; then
    aws_read "$region" secretsmanager delete-secret --secret-id "$arn" --force-delete-without-recovery >/dev/null
  fi
}

# Only the regular stack lifecycle may delete DNS, NAT, routes, IAM and ECS.
# Tagged leftovers from those services block success and need scoped review.
orphan_pass() {
  local region=$1 name=$2 inventory=$3 ordered arn prefix kind id
  prefix="arn:aws:ec2:$region:$account:"
  ordered=$(jq -r '
    def priority:
      if contains(":instance/") then 0
      elif contains(":network-interface/") then 1
      elif contains(":volume/") or contains(":elastic-ip/") or contains(":secret:") then 2
      elif contains(":security-group/") or contains(":subnet/") then 3
      elif contains(":vpc/") then 4
      else 5 end;
    map(.ResourceARN) | sort_by(priority)[]' <<<"$inventory") || return
  while IFS= read -r arn; do
    [[ -n $arn ]] || continue
    case "$arn" in
      "$prefix"instance/*) continue ;; # The EC2 live inventory has already verified termination.
      "$prefix"network-interface/*|"$prefix"volume/*|"$prefix"elastic-ip/*|"$prefix"subnet/*|"$prefix"security-group/*|"$prefix"vpc/*)
        kind=${arn#"$prefix"}; kind=${kind%%/*}; id=${arn##*/}
        delete_ec2_orphan "$region" "$name" "$kind" "$id" || return ;;
      "arn:aws:secretsmanager:$region:$account:secret:"*)
        delete_retained_secret "$region" "$name" "$arn" || return ;;
      *) fail "non-deleted resource awaiting CloudFormation reconciliation for $name in $region"; return 1 ;;
    esac
  done <<<"$ordered"
}

clean_scope() {
  local region=$1 name=$2 stacks_json stack_id stack='null' status inventory nodes instance vpc resources stack_vpc
  local verified_stack_id=''
  local workspace_only issued=false
  while (( SECONDS < deadline )); do
    stacks_json=$(stacks "$region") || return
    stack_id=$(jq -r --arg name "$name" '
      map(select(.StackName == $name)) | if length == 0 then "" elif length == 1 then .[0].StackId
        else error("ambiguous stack") end' <<<"$stacks_json") || return
    workspace_only=false
    if [[ -n $stack_id ]]; then
      stack=$(owned_stack "$region" "$name" "$stack_id") || return
      verified_stack_id=$stack_id
      workspace_only=true
    fi
    nodes=$(live_instances "$region" "$name" "$workspace_only") || return
    if jq -e 'length != 0' <<<"$nodes" >/dev/null; then
      if "$workspace_only"; then
        resources=$(stack_resources "$region" "$stack_id") || return
        stack_vpc=$(jq -er 'map(select(.LogicalResourceId == "Vpc" and .ResourceType == "AWS::EC2::VPC"))
          | select(length == 1) | .[0].PhysicalResourceId | select(test("^vpc-[a-f0-9]+$"))' <<<"$resources") ||
          { fail "could not verify the qualification stack VPC for $name in $region"; return 1; }
      fi
      while IFS=$'\t' read -r instance vpc; do
        [[ -n $instance ]] || continue
        if "$workspace_only" && [[ $vpc != "$stack_vpc" ]]; then
          fail "out-of-stack instance does not belong to the qualification VPC for $name in $region"
          return 1
        fi
        assert_vpc "$region" "$name" "$vpc" || return
        aws_read "$region" ec2 terminate-instances --instance-ids "$instance" >/dev/null || return
      done < <(jq -r '.[] | select(.State.Name != "shutting-down") | [.InstanceId, .VpcId] | @tsv' <<<"$nodes")
    elif [[ -n $stack_id ]]; then
      status=$(jq -er '.StackStatus' <<<"$stack") || return
      case "$status" in
        REVIEW_IN_PROGRESS|*_COMPLETE|*_FAILED)
          if "$issued" && [[ $status == DELETE_FAILED ]]; then
            fail "CloudFormation delete failed for $name in $region; inspect stack events before retry"
            return 1
          fi
          if ! "$issued"; then
            if jq -e '.EnableTerminationProtection == true' <<<"$stack" >/dev/null; then
              aws_read "$region" cloudformation update-termination-protection \
                --stack-name "$stack_id" --no-enable-termination-protection >/dev/null || return
            fi
            aws_read "$region" cloudformation delete-stack --stack-name "$stack_id" >/dev/null || return
            issued=true
          fi ;;
        *_IN_PROGRESS) ;;
        *) fail "unrecognized stack status for $name in $region"; return 1 ;;
      esac
    else
      if [[ -n $verified_stack_id ]]; then
        verify_stack_deleted "$region" "$name" "$verified_stack_id" || return
        verified_stack_id=''
      fi
      inventory=$(tagged "$region" "$name") || return
      # An EC2 instance ARN can still be indexed after native EC2 reports
      # termination. All other tagged artifacts must disappear.
      if jq -e --arg prefix "arn:aws:ec2:$region:$account:instance/" \
        'all(.[]; .ResourceARN | startswith($prefix))' <<<"$inventory" >/dev/null; then
        verify_nat_deleted "$region" "$name"
        return
      fi
      orphan_pass "$region" "$name" "$inventory" || return
    fi
    sleep 8
  done
  fail "cleanup timed out for $name in $region; rerun independent cleanup after inspecting AWS"
}

clean_run() {
  local name=$1 region result=0
  [[ $name =~ $run_pattern ]] || { fail 'invalid run ownership name'; return 1; }
  for region in "${regions[@]}"; do
    clean_scope "$region" "$name-p" || result=1
    clean_scope "$region" "$name" || result=1
  done
  if (( result == 0 )); then printf 'Verified AWS teardown for %s in four regions.\n' "$name"; fi
  return "$result"
}

sweep() {
  local names name id attempt current previous result=0
  names=$(discover | sort -u) || return
  while IFS= read -r name; do
    [[ -n $name ]] || continue
    id=${name#sbq-}; attempt=${id##*-}; id=${id%-*}
    current=$(lookup_run "$id") || { result=1; continue; }
    if ! jq -e '.status == "completed"' <<<"$current" >/dev/null; then continue; fi
    previous=$(lookup_run "$id" "$attempt") || { result=1; continue; }
    # Delay independent cleanup until GitHub reports the attempt finished and
    # the API update has been stable for 30 minutes.
    if jq -e '.status == "completed" and (.conclusion | type == "string") and
      (now - (.updated_at | fromdateiso8601) >= 1800)' <<<"$previous" >/dev/null; then
      clean_run "$name" || result=1
    fi
  done <<<"$names"
  return "$result"
}

[[ $# == 1 && $1 =~ ^(run|sweep|empty)$ ]] || { fail 'usage: cleanup.sh run|sweep|empty'; exit 2; }
[[ ${GITHUB_REPOSITORY:-} == "$repo" && ${GITHUB_REF:-} == refs/heads/main &&
  ${GITHUB_WORKFLOW_REF:-} == "$repo/$workflow@refs/heads/main" ]] ||
  { fail 'cleanup requires the protected qualification workflow on main'; exit 1; }
assert_account
case $1 in
  run)
    [[ ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]] || { fail 'per-run cleanup requires a maintainer dispatch'; exit 1; }
    name="sbq-${GITHUB_RUN_ID:-}-${GITHUB_RUN_ATTEMPT:-}"
    [[ $name =~ $run_pattern ]] || { fail 'invalid current qualification run identity'; exit 1; }
    lookup_run "$GITHUB_RUN_ID" "$GITHUB_RUN_ATTEMPT" >/dev/null
    clean_run "$name" ;;
  sweep) sweep ;;
  empty)
    remaining=$(discover)
    [[ -z $remaining ]] || { fail 'qualification resources remain; run independent cleanup before provisioning'; exit 1; } ;;
esac
