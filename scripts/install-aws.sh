#!/usr/bin/env bash
# jq and JMESPath expand their own variables and literals.
# shellcheck disable=SC2016
{
# Bash parses the complete block before executing any part of a piped installer.
install_help() {
  cat <<'HELP'
Usage: install.sh [--version <public-tag>] [--work-dir <directory>] [--resume]

Review and install a public ShiftBox release in your AWS account.
Omit --version for stable. Use --version to select a specific public RC or stable.
HELP
}

install_fail() {
  printf 'ShiftBox install: %s\n' "$*" >&2
  return 1
}

install_release() {
  local requested=$1 file=$2
  jq -er --arg requested "$requested" '
    def core: "v(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)";
    def stable: test("^" + core + "\\z");
    def candidate: test("^" + core + "-rc[.][1-9][0-9]*\\z");
    select(.draft == false and .immutable == true and (.published_at | type == "string" and length > 0)) |
    select((.id | type == "number" and . > 0) and
      .html_url == "https://github.com/jflatten/ShiftBox/releases/tag/\(.tag_name)") |
    select(([.assets[].name] | sort) ==
      ["distribution-manifest.json", "install.sh", "shiftbox-aws-cloudformation.yaml", "shiftbox-aws-support.tar.gz"]) |
    select(all(.assets[]; .id | type == "number" and . > 0)) |
    select(if $requested == "" then .prerelease == false and (.tag_name | stable)
      else .tag_name == $requested and
        ((.prerelease == true and (.tag_name | candidate)) or (.prerelease == false and (.tag_name | stable)))
      end) |
    .tag_name
  ' <"$file"
}

install_identity() {
  local state=$1 account=$2 region=$3 stack=$4 release=$5
  jq -e --arg account "$account" --arg region "$region" --arg stack "$stack" --arg release "$release" '
    .account == $account and .region == $region and .stack == $stack and .release == $release
  ' <"$state" >/dev/null
}

install_preview() {
  local changes=$1 events=$2 phase=$3
  if ! jq -e '
    (.OperationEvents | type == "array") and all(.OperationEvents[];
      .EventType != "VALIDATION_ERROR" or .ValidationFailureMode == "WARN")
  ' <"$events" >/dev/null; then
    jq -r '.OperationEvents[]? | .ValidationStatusReason // .ResourceStatusReason // empty' <"$events" >&2
    install_fail "CloudFormation validation failed for the $phase change set"
    return 1
  fi
  if ! jq -e --arg phase "$phase" '
    def dns_resource: .ResourceType | startswith("AWS::Route53::") or startswith("AWS::ServiceDiscovery::");
    def only_adds($expected):
      length == ($expected | length) and
      all(.[]; .Action == "Add") and
      (map({key:.LogicalResourceId,value:.ResourceType}) | from_entries) == $expected;
    .Status == "CREATE_COMPLETE" and .ExecutionStatus == "AVAILABLE" and
    .Capabilities == ["CAPABILITY_NAMED_IAM"] and (.Changes | type == "array" and length > 0) and
    all(.Changes[];
      .Type == "Resource" and (.ResourceChange |
        .Action == "Add" or (.Action == "Modify" and
          (.Replacement == "False" or .ResourceType == "AWS::ECS::TaskDefinition")))) and
    ([.Changes[].ResourceChange] |
      if $phase == "foundation" then
        all(.[]; .Action == "Add" and (dns_resource | not) and
          .ResourceType != "AWS::ECS::Service" and .ResourceType != "AWS::ECS::TaskDefinition")
      elif $phase == "dns-zones" then
        only_adds({PrivateZone:"AWS::Route53::HostedZone",DiscoveryNamespace:"AWS::ServiceDiscovery::PrivateDnsNamespace"})
      elif $phase == "dns-services" then
        only_adds({PostgresDiscovery:"AWS::ServiceDiscovery::Service",ControllerDiscovery:"AWS::ServiceDiscovery::Service"})
      elif $phase == "database" then
        map(select(dns_resource)) | only_adds({PrivateRecords:"AWS::Route53::RecordSetGroup"})
      elif $phase == "active" then
        all(.[]; dns_resource | not)
      else false end)
  ' <"$changes" >/dev/null; then
    install_fail "the $phase change set is unavailable, lacks named IAM acknowledgement, or contains unexpected changes"
    return 1
  fi
  jq -r '.OperationEvents[]? | select(.ValidationFailureMode == "WARN") |
    .ValidationStatusReason // .ResourceStatusReason // "CloudFormation validation warning"' <"$events"
  jq -r '.Changes[].ResourceChange |
    [.Action, .LogicalResourceId, .ResourceType, (.Replacement // "-")] | @tsv' <"$changes"
}

install_fetch() {
  local url=$1 destination=$2 effective
  effective=$(curl --silent --show-error --fail --location --max-redirs 3 --proto '=https' \
    --proto-redir '=https' --connect-timeout 10 --max-time 120 \
    --output "$destination" --write-out '%{url_effective}' "$url") || return 1
  case "$url" in
    https://api.github.com/*) [ "$effective" = "$url" ] ;;
    https://github.com/jflatten/ShiftBox/releases/download/*)
      case "$effective" in
        "$url"|https://release-assets.githubusercontent.com/*|https://objects.githubusercontent.com/*) return 0 ;;
        *) install_fail "unexpected GitHub release download destination" ;;
      esac ;;
    *) [ "$effective" = "$url" ] ;;
  esac
}

install_tag_commit() {
  local tag=$1 scratch=$2 result type sha
  result="$scratch/tag.json"
  install_fetch "https://api.github.com/repos/jflatten/ShiftBox/git/ref/tags/$tag" "$result" || return 1
  jq -e --arg tag "$tag" '.ref == ("refs/tags/" + $tag)' <"$result" >/dev/null || return 1
  type=$(jq -er '.object.type' <"$result") || return 1
  sha=$(jq -er '.object.sha | select(test("^[0-9a-f]{40}\\z"))' <"$result") || return 1
  if [ "$type" = tag ]; then
    install_fetch "https://api.github.com/repos/jflatten/ShiftBox/git/tags/$sha" "$result" || return 1
    jq -er --arg tag "$tag" --arg sha "$sha" '
      select(.sha == $sha and .tag == $tag and .object.type == "commit") |
      .object.sha | select(test("^[0-9a-f]{40}\\z"))
    ' <"$result"
  elif [ "$type" = commit ] && [[ "$tag" == *-rc.* ]]; then
    printf '%s\n' "$sha"
  else
    install_fail "the stable ref does not identify an annotated release tag"
  fi
}

install_require_tools() {
  local tool
  for tool in curl jq openssl tar aws session-manager-plugin; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      install_fail "required tool '$tool' is missing"
      return 1
    fi
  done
  if ! aws cloudformation describe-events --generate-cli-skeleton input >/dev/null 2>&1; then
    install_fail "AWS CLI v2 must support CloudFormation describe-events"
  fi
}

install_prompt() {
  local message=$1
  printf '%s ' "$message" >&3
  IFS= read -r REPLY <&3
}

install_artifacts() {
  local requested=$1 region=$2 publisher=$3 directory=$4 release_api release_name source name template_url
  local manifest="$directory/distribution-manifest.json"
  release_api=https://api.github.com/repos/jflatten/ShiftBox/releases/latest
  if [ -n "$requested" ]; then
    release_api="https://api.github.com/repos/jflatten/ShiftBox/releases/tags/$requested"
  fi
  if ! install_fetch "$release_api" "$directory/release.json" ||
      ! release_name=$(install_release "$requested" "$directory/release.json"); then
    install_fail "eligible release unavailable. Select a published version or inspect the GitHub Releases page."
    return 1
  fi
  source=$(install_tag_commit "$release_name" "$directory") || return 1
  for name in distribution-manifest.json install.sh shiftbox-aws-cloudformation.yaml shiftbox-aws-support.tar.gz; do
    install_fetch "https://github.com/jflatten/ShiftBox/releases/download/$release_name/$name" "$directory/$name" || return 1
  done
  manifest_validate "$manifest" "$release_name" "$source" "$publisher" || return 1
  manifest_verify_assets "$manifest" "$directory" || return 1
  template_url=$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' <"$manifest") || return 1
  install_fetch "$template_url" "$directory/hosted-template.yaml" || return 1
  if ! cmp -s "$directory/hosted-template.yaml" "$directory/shiftbox-aws-cloudformation.yaml"; then
    install_fail "the hosted template does not match the inspected release template"
    return 1
  fi
  printf '%s\n' "$release_name"
}

install_verify_aws_images() {
  local manifest=$1 region=$2 workspace host publisher root snapshot now
  workspace=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.amiId' <"$manifest") || return 1
  host=$(jq -er --arg region "$region" '.regions[$region].hostAmiId' <"$manifest") || return 1
  publisher=$(jq -er '.publisherAccountId' <"$manifest") || return 1
  root=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.rootDeviceName' <"$manifest") || return 1
  snapshot=$(jq -er --arg region "$region" '.regions[$region].workspaceImage.rootSnapshotId' <"$manifest") || return 1
  now=$(date -u '+%Y-%m-%dT%H:%M:%SZ') || return 1
  if ! aws --region "$region" ec2 describe-images --owners "$publisher" --image-ids "$workspace" --output json |
      jq -e --arg ami "$workspace" --arg owner "$publisher" --arg root "$root" --arg snapshot "$snapshot" --arg now "$now" '
        (.Images | length) == 1 and (.Images[0] |
          .ImageId == $ami and .OwnerId == $owner and .RootDeviceName == $root and
          .State == "available" and .Public == true and .Architecture == "x86_64" and
          .VirtualizationType == "hvm" and .RootDeviceType == "ebs" and
          ((.DeprecationTime // $now) | sub("[.][0-9]+Z$"; "Z")) >= $now and
          ([.BlockDeviceMappings[] | select(.Ebs != null)] | length) == 1 and
          any(.BlockDeviceMappings[]; .DeviceName == $root and .Ebs.SnapshotId == $snapshot and .Ebs.Encrypted != true))' >/dev/null; then
    install_fail "the official workspace AMI could not be verified in $region"
    return 1
  fi
  if ! aws --region "$region" ec2 describe-images --owners amazon --image-ids "$host" --output json |
      jq -e --arg ami "$host" '(.Images | length) == 1 and
        (.Images[0] | .ImageId == $ami and .State == "available" and .Architecture == "x86_64")' >/dev/null; then
    install_fail "the pinned ECS host AMI could not be verified in $region"
  fi
}

install_store() {
  local state=$1
  shift
  jq "$@" <"$state" >"$state.new" && mv -f -- "$state.new" "$state"
}

install_bind_stack() {
  local state=$1 stack=$2
  install_store "$state" --arg stack "$stack" '
    ("arn:aws:cloudformation:" + .region + ":" + .account + ":stack/" + .stackName + "/") as $prefix |
    if ($stack | startswith($prefix)) and
        ($stack | ltrimstr($prefix) | test("^[0-9a-f-]{36}\\z")) and
        (.stack == "" or .stack == $stack) then .stack = $stack
    else error("CloudFormation stack ID differs from the reviewed account, region, or stack") end'
}

install_current_account() {
  local account=$1 region=$2 actual
  actual=$(aws --region "$region" sts get-caller-identity --query Account --output text) || return 1
  [ "$actual" = "$account" ] || install_fail "AWS credentials now identify another account"
}

install_describe_stack() {
  local region=$1 stack=$2 output=$3
  aws --region "$region" cloudformation describe-stacks --stack-name "$stack" --output json >"$output" || return 1
  jq -e '(.Stacks | length) == 1' <"$output" >/dev/null
}

install_output() {
  jq -er --arg key "$2" '.Stacks[0].Outputs[]? | select(.OutputKey == $key) |
    .OutputValue | select(type == "string" and length > 0)' <"$1"
}

install_wait_status() {
  local region=$1 stack=$2 operation=$3
  if ! aws --region "$region" cloudformation wait "$operation-complete" --stack-name "$stack"; then
    aws --region "$region" cloudformation describe-events --stack-name "$stack" \
      --filters FailedEvents=true --output json >&2 || true
    install_fail "stack operation did not complete. Inspect the CloudFormation operation and resume by stack ID."
  fi
}

install_change() {
  local state=$1 phase=$2 type=$3 scratch=$4 allow_submit=${5:-true}
  local account region stack name changes events desired digest change_name phase_record change_id stack_id decision
  account=$(jq -er '.account' <"$state") || return 1
  region=$(jq -er '.region' <"$state") || return 1
  stack=$(jq -er '.stack' <"$state") || return 1
  name=$(jq -er '.stackName' <"$state") || return 1
  desired="$scratch/parameters.json"
  changes="$scratch/change-set.json"
  events="$scratch/change-events.json"
  jq --arg phase "$phase" '
    .parameters + {Phase:$phase} |
    to_entries | map({ParameterKey:.key,ParameterValue:(.value | tostring)})
  ' <"$state" >"$desired" || return 1
  digest=$({ cat "$desired"; jq -er '.manifestSha256' <"$state"; } | openssl dgst -sha256 -r) || return 1
  digest=${digest%% *}
  change_name="shiftbox-$phase-${digest:0:24}"
  phase_record="ShiftBox reviewed $phase parameters sha256:$digest"

  install_current_account "$account" "$region" || return 1
  if ! aws --region "$region" cloudformation describe-change-set --stack-name "${stack:-$name}" \
      --change-set-name "$change_name" --output json >"$changes" 2>"$scratch/change-error"; then
    if ! grep -Eq 'ChangeSetNotFound|Change set .* does not exist|Stack .* does not exist' "$scratch/change-error"; then
      cat "$scratch/change-error" >&2
      return 1
    fi
    if [ "$allow_submit" = false ]; then
      install_fail "the existing review stack has no matching recorded change set"
      return 1
    fi
    printf 'This change set previews %s in account %s, region %s. Review the named IAM resources and their permissions boundaries.\n' "$phase" "$account" "$region"
    install_prompt 'Type CAPABILITY_NAMED_IAM to acknowledge this change set:' || return 1
    if [ "$REPLY" != CAPABILITY_NAMED_IAM ]; then
      install_fail "IAM capability acknowledgement was declined"
      return 1
    fi
    install_current_account "$account" "$region" || return 1
    aws --region "$region" cloudformation create-change-set \
      --stack-name "${stack:-$name}" --change-set-name "$change_name" --change-set-type "$type" \
      --description "$phase_record" --client-token "$change_name" --capabilities CAPABILITY_NAMED_IAM \
      --template-url "$(jq -er '.templateUrl' <"$state")" --parameters "file://$desired" \
      --tags "Key=project,Value=shiftbox" "Key=cluster_name,Value=$name" --output json >"$scratch/change-result" || return 1
    stack_id=$(jq -er '.StackId' <"$scratch/change-result") || return 1
    install_bind_stack "$state" "$stack_id" || return 1
  fi
  stack=$(jq -er '.stack' <"$state") || return 1
  aws --region "$region" cloudformation wait change-set-create-complete --stack-name "${stack:-$name}" \
    --change-set-name "$change_name" || true
  aws --region "$region" cloudformation describe-change-set --stack-name "${stack:-$name}" \
    --change-set-name "$change_name" --output json >"$changes" || return 1
  stack_id=$(jq -er --arg description "$phase_record" --arg name "$change_name" '
    select(.Description == $description and .ChangeSetName == $name) | .StackId' <"$changes") || return 1
  install_bind_stack "$state" "$stack_id" || return 1
  change_id=$(jq -er --arg prefix "arn:aws:cloudformation:$region:$account:changeSet/$change_name/" '
    .ChangeSetId | select(startswith($prefix))' <"$changes") || return 1
  if ! jq -e --slurpfile desired "$desired" '
    .Parameters as $parameters | all($desired[0][]; . as $target |
      any($parameters[]; .ParameterKey == $target.ParameterKey and .ParameterValue == $target.ParameterValue))
  ' <"$changes" >/dev/null; then
    install_fail "the change set parameters differ from the reviewed request"
    return 1
  fi
  aws --region "$region" cloudformation get-template --stack-name "$stack_id" --change-set-name "$change_id" \
    --template-stage Original --output json | jq -ej '.TemplateBody | strings' >"$scratch/change-template.yaml" || return 1
  if ! cmp -s "$scratch/change-template.yaml" "$scratch/template.yaml"; then
    install_fail "CloudFormation did not receive the exact verified template"
    return 1
  fi
  aws --region "$region" cloudformation describe-events --change-set-name "$change_id" --output json >"$events" || return 1
  install_preview "$changes" "$events" "$phase" || return 1
  jq '{Parameters, Capabilities, Changes}' <"$changes" || return 1
  printf 'Change set %s\n' "$change_id"
  install_prompt "Type EXECUTE $phase to execute this exact change set:" || return 1
  decision=$REPLY
  if [ "$decision" != "EXECUTE $phase" ]; then
    install_fail "change set remains unexecuted. Resume to review it again."
    return 1
  fi
  install_current_account "$account" "$region" || return 1
  aws --region "$region" cloudformation describe-change-set --change-set-name "$change_id" --output json >"$scratch/recheck.json" || return 1
  if [ "$(jq -cS . <"$changes")" != "$(jq -cS . <"$scratch/recheck.json")" ]; then
    install_fail "the change set changed after review. Resume to review its current state."
    return 1
  fi
  aws --region "$region" cloudformation execute-change-set --change-set-name "$change_id" \
    --client-request-token "$change_name" || return 1
  if [ "$type" = CREATE ]; then
    install_wait_status "$region" "$stack_id" stack-create
  else
    install_wait_status "$region" "$stack_id" stack-update
  fi
}

install_protect_stack() {
  local account=$1 region=$2 stack=$3 stack_file=$4 policy=$5
  if [ "$(jq -r '.Stacks[0].EnableTerminationProtection' <"$stack_file")" != true ]; then
    install_current_account "$account" "$region" || return 1
    aws --region "$region" cloudformation update-termination-protection --enable-termination-protection \
      --stack-name "$stack" >/dev/null || return 1
  fi
  install_current_account "$account" "$region" || return 1
  local actual expected
  actual=$(aws --region "$region" cloudformation get-stack-policy --stack-name "$stack" --output json | jq -cS '.StackPolicyBody | if type == "string" then fromjson else empty end') || return 1
  expected=$(jq -cS . <"$policy") || return 1
  if [ -z "$actual" ]; then
    aws --region "$region" cloudformation set-stack-policy --stack-name "$stack" --stack-policy-body "file://$policy" || return 1
    actual=$(aws --region "$region" cloudformation get-stack-policy --stack-name "$stack" --output json | jq -ecS '.StackPolicyBody | fromjson') || return 1
  fi
  [ "$actual" = "$expected" ] || install_fail "the stack policy differs from the verified storage protection policy"
}

install_certificate() (
  set -euo pipefail
  local directory=$1 authority=$2 common_name=$3 names=$4 scratch
  if [ -d "$directory" ]; then
    openssl verify -CAfile "$directory/ca.pem" "$directory/leaf.pem" >/dev/null || return 1
    return 0
  fi
  scratch=$(mktemp -d "${directory}.XXXXXX")
  trap 'rm -rf "$scratch"' EXIT
  cat >"$scratch/ca.cnf" <<'CA'
[req]
distinguished_name = dn
[dn]
[extensions]
basicConstraints = critical,CA:TRUE,pathlen:0
keyUsage = critical,keyCertSign,cRLSign
CA
  printf 'basicConstraints = critical,CA:FALSE\nkeyUsage = critical,digitalSignature,keyEncipherment\nextendedKeyUsage = serverAuth\nsubjectAltName = %s\n' "$names" >"$scratch/leaf.cnf"
  openssl req -new -x509 -nodes -newkey rsa:3072 -days 1825 -sha256 \
    -subj "/CN=$authority" -config "$scratch/ca.cnf" -extensions extensions \
    -keyout "$scratch/ca.key" -out "$scratch/ca.pem" >"$scratch/openssl.log" 2>&1 || return 1
  openssl req -new -nodes -newkey rsa:2048 -subj "/CN=$common_name" \
    -keyout "$scratch/leaf.key" -out "$scratch/leaf.csr" >>"$scratch/openssl.log" 2>&1 || return 1
  openssl x509 -req -days 397 -sha256 -in "$scratch/leaf.csr" \
    -CA "$scratch/ca.pem" -CAkey "$scratch/ca.key" -CAcreateserial \
    -extfile "$scratch/leaf.cnf" -out "$scratch/leaf.pem" >>"$scratch/openssl.log" 2>&1 || return 1
  rm -f -- "$scratch/openssl.log" "$scratch/leaf.csr"
  mv -- "$scratch" "$directory"
)

install_database_tls() (
  set -euo pipefail
  local state=$1 stack_file=$2 directory=$3 scratch=$4
  local account region host secret metadata ca token
  account=$(jq -er '.account' <"$state")
  region=$(jq -er '.region' <"$state")
  host=$(install_output "$stack_file" PostgresHostname)
  secret=$(install_output "$stack_file" PostgresTlsSecretArn)
  case "$secret" in "arn:aws:secretsmanager:$region:$account:secret:"*) ;; *) return 1 ;; esac
  if ! [[ "$host" =~ ^postgres\.[a-z0-9][a-z0-9.-]+$ ]]; then return 1; fi
  install_current_account "$account" "$region"
  metadata=$(aws --region "$region" secretsmanager describe-secret --secret-id "$secret" --output json)
  if ! jq -e 'any(.VersionIdsToStages[]?[]; . == "AWSCURRENT")' <<<"$metadata" >/dev/null; then
    install_certificate "$directory" 'ShiftBox PostgreSQL CA' "$host" "DNS:$host"
    install_prompt "Type STORE TLS to populate the empty PostgreSQL TLS secret $secret:"
    [ "$REPLY" = 'STORE TLS' ] || return 1
    install_current_account "$account" "$region"
    jq -n --rawfile certificate "$directory/leaf.pem" --rawfile key "$directory/leaf.key" \
      '{certificate:$certificate,private_key:$key}' >"$scratch/postgres-tls.json"
    token=$(manifest_sha256 "$scratch/postgres-tls.json")
    aws --region "$region" secretsmanager put-secret-value --secret-id "$secret" \
      --client-request-token "$token" --secret-string "file://$scratch/postgres-tls.json" >/dev/null
    rm -f -- "$scratch/postgres-tls.json"
  else
    if [ ! -f "$directory/ca.pem" ] || [ ! -f "$directory/leaf.key" ]; then
      install_fail "the stack already has PostgreSQL TLS. Recover the original CA or continue through the console."
      return 1
    fi
    # Derive the same request token after a successful write whose response was lost.
    jq -n --rawfile certificate "$directory/leaf.pem" --rawfile key "$directory/leaf.key" \
      '{certificate:$certificate,private_key:$key}' >"$scratch/postgres-tls.json"
    token=$(manifest_sha256 "$scratch/postgres-tls.json")
    rm -f -- "$scratch/postgres-tls.json"
    if ! jq -e --arg token "$token" '(.VersionIdsToStages[$token] // []) | index("AWSCURRENT") != null' <<<"$metadata" >/dev/null; then
      install_fail "the stored PostgreSQL TLS version differs from the locally prepared certificate"
      return 1
    fi
  fi
  ca=$(tr '\n' '|' <"$directory/ca.pem")
  install_store "$state" --arg ca "$ca" '.parameters.PostgresCaPem = $ca'
)

install_prompt_parameters() {
  local state=$1 region=$2 name zone mode suffix cidr vpc github
  install_prompt 'Enter a new lowercase deployment name (2–32 characters):' || return 1
  name=$REPLY
  [[ "$name" =~ ^[a-z][a-z0-9-]{0,30}[a-z0-9]$ ]] || return 1
  aws --region "$region" ec2 describe-availability-zones --query 'AvailabilityZones[?State==`available`].ZoneName' --output text || return 1
  install_prompt 'Choose an available Availability Zone:' || return 1
  zone=$REPLY
  if ! [[ "$zone" =~ ^${region}[a-z]$ ]]; then return 1; fi
  install_prompt 'Enter a nonoverlapping VPC CIDR [10.63.0.0/16]:' || return 1
  vpc=${REPLY:-10.63.0.0/16}
  install_prompt 'Choose private access mode [tailscale] or private-link:' || return 1
  mode=${REPLY:-tailscale}
  cidr=''
  if [ "$mode" = tailscale ]; then
    suffix="sb-$(openssl rand -hex 8).internal"
  elif [ "$mode" = private-link ]; then
    install_prompt 'Enter the private DNS suffix:' || return 1
    suffix=$REPLY
    install_prompt 'Enter the approved IPv4 source CIDR for browser access:' || return 1
    cidr=$REPLY
    if ! [[ "$cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([1-9]|[12][0-9]|3[0-2])$ ]]; then return 1; fi
  else
    return 1
  fi
  if ! [[ "$suffix" =~ ^[a-z0-9][a-z0-9.-]+[a-z0-9]$ ]]; then return 1; fi
  printf 'Management origin after setup: https://shiftbox.%s:8443\n' "$suffix"
  install_prompt 'Enter the optional operator-owned GitHub App credentials secret ARN [none]:' || return 1
  github=$REPLY
  install_store "$state" --arg name "$name" --arg zone "$zone" --arg vpc "$vpc" --arg suffix "$suffix" \
    --arg mode "$mode" --arg cidr "$cidr" --arg github "$github" '
    .stackName = $name | .parameters = {DeploymentName:$name,AvailabilityZone:$zone,VpcCidr:$vpc,
      DnsSuffix:$suffix,AccessMode:$mode,GitHubAppSecretArn:$github,RoleBoundaryMode:"managed"} |
    if $mode == "private-link" then .parameters.OperatorCidr = $cidr else . end'
}

install_record_stage() {
  local state=$1 stack_file=$2 expected=${3:-} stack record
  stack=$(jq -er '.stack' <"$state") || return 1
  record=$(jq -ce --arg stack "$stack" --arg expected "$expected" '
    def output($outputs; $key; $pattern):
      [$outputs[] | select(.OutputKey == $key) | .OutputValue] |
      if length == 1 and (.[0] | type == "string" and test($pattern)) then .[0]
      else error("missing or invalid CloudFormation output \\($key)") end;
    select((.Stacks | length) == 1) | .Stacks[0] |
    select(.StackId == $stack and
      (.StackStatus | IN("CREATE_COMPLETE","UPDATE_COMPLETE","UPDATE_ROLLBACK_COMPLETE")) and
      ([.Parameters[] | select(.ParameterKey == "RoleBoundaryMode") | .ParameterValue] == ["managed"])) |
    .Outputs as $outputs |
    output($outputs; "Phase"; "^(foundation|dns-zones|dns-services|database|active)\\z") as $phase |
    select($expected == "" or $phase == $expected) |
    "([0-9a-f]{8}|[0-9a-f]{17})\\z" as $ec2_id |
    {phase:$phase, vpc:output($outputs; "VpcId"; "^vpc-" + $ec2_id),
      volume:output($outputs; "InstallationVolumeId"; "^vol-" + $ec2_id),
      dns:(
        (if $phase == "foundation" then {} else
          "^(/hostedzone/)?Z[A-Z0-9]{1,31}\\z" as $zone |
          {PrivateZoneId:(output($outputs; "PrivateZoneId"; $zone) | ltrimstr("/hostedzone/")),
           DiscoveryHostedZoneId:(output($outputs; "DiscoveryHostedZoneId"; $zone) | ltrimstr("/hostedzone/")),
           DiscoveryNamespaceId:output($outputs; "DiscoveryNamespaceId"; "^ns-[a-z0-9]+\\z")} |
          select(.PrivateZoneId != .DiscoveryHostedZoneId)
        end) +
        (if ($phase | IN("foundation","dns-zones")) then {} else
          "^srv-[a-z0-9]+\\z" as $service |
          {PostgresDiscoveryServiceId:output($outputs; "PostgresDiscoveryServiceId"; $service),
           ControllerDiscoveryServiceId:output($outputs; "ControllerDiscoveryServiceId"; $service)} |
          select(.PostgresDiscoveryServiceId != .ControllerDiscoveryServiceId)
        end)
      )}
  ' <"$stack_file") || { install_fail "could not verify the completed stack phase and its resource IDs"; return 1; }
  install_store "$state" --argjson observed "$record" '
    if (.volume // $observed.volume) != $observed.volume then error("the installation volume changed")
    elif (.vpc // $observed.vpc) != $observed.vpc then error("the installation VPC changed")
    elif ((.dnsIdentity // {}) | to_entries | any(.[]; $observed.dns[.key] != .value)) then
      error("a recorded private DNS resource changed or disappeared")
    else .volume = $observed.volume | .vpc = $observed.vpc | .dnsIdentity = $observed.dns end
  ' || return 1
  jq -er '.phase' <<<"$record"
}

install_advance() {
  local state=$1 phase=$2 region=$3 stack=$4 stack_file=$5 scratch=$6
  install_change "$state" "$phase" UPDATE "$scratch" || return 1
  install_describe_stack "$region" "$stack" "$stack_file" || return 1
  install_record_stage "$state" "$stack_file" "$phase"
}

install_run_stages() (
  set -euo pipefail
  local state=$1 work=$2 scratch=$3 phase stack status support account region release dns mode
  local stack_file="$scratch/stack.json" protection="$scratch/support/stack-policy.json"
  account=$(jq -er '.account' <"$state")
  region=$(jq -er '.region' <"$state")
  release=$(jq -er '.release' <"$state")
  stack=$(jq -er '.stack' <"$state")
  install_current_account "$account" "$region"
  mkdir -- "$scratch/support"
  support="$work/release/shiftbox-aws-support.tar.gz"
  tar -xzf - -C "$scratch/support" <"$support"

  if [ -z "$stack" ]; then
    local name allow_submit=true
    name=$(jq -er '.stackName' <"$state")
    if install_describe_stack "$region" "$name" "$stack_file" 2>"$scratch/stack-error"; then
      # The exact recorded operation can bind a CREATE review stack after a lost response.
      if [ "$(jq -r '.Stacks[0].StackStatus' <"$stack_file")" != REVIEW_IN_PROGRESS ]; then
        install_fail "an existing stack owns this name. Choose another name or recover the original installer state."
        return 1
      fi
      if [ ! -f "$work/create-requested" ]; then
        install_fail "an unrelated review stack owns this name"; return 1
      fi
      allow_submit=false
    elif ! grep -Eq 'Stack .* does not exist' "$scratch/stack-error"; then
      cat "$scratch/stack-error" >&2
      return 1
    fi
    : >"$work/create-requested"
    install_change "$state" foundation CREATE "$scratch" "$allow_submit"
    stack=$(jq -er '.stack' <"$state")
  fi
  install_describe_stack "$region" "$stack" "$stack_file"
  install_identity "$state" "$account" "$region" "$(jq -er '.Stacks[0].StackId' <"$stack_file")" "$release"
  status=$(jq -er '.Stacks[0].StackStatus' <"$stack_file")
  case "$status" in
    REVIEW_IN_PROGRESS) install_change "$state" foundation CREATE "$scratch" ;;
    CREATE_IN_PROGRESS) install_wait_status "$region" "$stack" stack-create ;;
    UPDATE_IN_PROGRESS|UPDATE_COMPLETE_CLEANUP_IN_PROGRESS) install_wait_status "$region" "$stack" stack-update ;;
    CREATE_COMPLETE|UPDATE_COMPLETE|UPDATE_ROLLBACK_COMPLETE) ;;
    *) install_fail "stack is $status. Inspect CloudFormation events before resuming."; return 1 ;;
  esac
  install_describe_stack "$region" "$stack" "$stack_file"
  phase=$(install_record_stage "$state" "$stack_file")
  install_protect_stack "$account" "$region" "$stack" "$stack_file" "$protection"
  if [ "$phase" = foundation ]; then
    phase=$(install_advance "$state" dns-zones "$region" "$stack" "$stack_file" "$scratch")
  fi
  if [ "$phase" = dns-zones ]; then
    phase=$(install_advance "$state" dns-services "$region" "$stack" "$stack_file" "$scratch")
  fi
  if [ "$phase" = dns-services ]; then
    install_database_tls "$state" "$stack_file" "$work/database-tls" "$scratch"
    phase=$(install_advance "$state" database "$region" "$stack" "$stack_file" "$scratch")
  fi
  if [ "$phase" = database ]; then
    dns=$(jq -er '.parameters.DnsSuffix' <"$state")
    mode=$(jq -er '.parameters.AccessMode' <"$state")
    if [ "$mode" = tailscale ]; then
      local router route
      router=$(install_output "$stack_file" TailscaleRouterInstanceId)
      route=$(install_output "$stack_file" TailscaleAdvertisedRoute)
      printf 'Open a router session with aws --region %s ssm start-session --target %s.\n' "$region" "$router"
      printf 'On the router run sudo tailscale up --accept-dns=false --advertise-routes=%s.\n' "$route"
      printf 'Approve that service route in your tailnet. Use the router tailnet IP as a restricted nameserver for %s.\n' "$dns"
      printf 'Allow TCP/UDP 53 to the router and TCP 8443 to the service subnet for intended clients.\n'
      install_certificate "$work/browser-tls" 'ShiftBox Browser CA' "shiftbox.$dns" \
        "DNS:shiftbox.$dns,DNS:*.ide.$dns,DNS:*.preview.$dns,DNS:*.repair.$dns"
      openssl x509 -noout -sha256 -fingerprint -in "$work/browser-tls/ca.pem"
      printf 'Trust %s once, then supply %s and %s through the protected browser setup.\n' \
        "$work/browser-tls/ca.pem" "$work/browser-tls/leaf.pem" "$work/browser-tls/leaf.key"
    else
      printf 'Configure approved private routing, DNS for %s and trusted browser leaf/key.\n' "$dns"
    fi
    printf 'The authorized bootstrap identity needs this scoped setup policy: %s\n' \
      "$(install_output "$stack_file" SetupOperatorPolicyArn)"
    install_current_account "$account" "$region"
    sh "$scratch/support/setup.sh" "$stack" "$region" "$account" </dev/tty >/dev/tty 2>/dev/tty
    phase=$(install_advance "$state" active "$region" "$stack" "$stack_file" "$scratch")
  fi
  [ "$phase" = active ] || return 1
  printf 'Open your private management origin: %s\n' "$(install_output "$stack_file" ManagementOrigin)"
  printf 'Sign in, validate the exact official AMI, and configure your pinned workspace profile. See the operator guide in the release.\n'
)

install_cleanup() {
  local directory=$1 scratch=$2 resume=$3
  if [ -n "$scratch" ]; then
    rm -rf -- "$scratch"
  fi
  rmdir -- "$directory/.install-lock"
  # An uncommitted first attempt has no AWS changes and can start again cleanly.
  if [ "$resume" = false ] && [ ! -e "$directory/state.json" ]; then
    rm -rf -- "$directory/release"
    rmdir -- "$directory" 2>/dev/null || true
  fi
}

install_aws() (
  set -euo pipefail
  umask 077
  export AWS_PAGER="" AWS_CLI_AUTO_PROMPT=off
  local requested='' directory=.shiftbox/aws-install resume=false scratch account region publisher release
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --help|-h) install_help; return 0 ;;
      --version|--work-dir)
        if [ "$#" -lt 2 ] || [[ "$2" == -* ]]; then
          install_fail "$1 requires a value"; return 2
        fi
        if [ "$1" = --version ]; then requested=$2; else directory=$2; fi
        shift ;;
      --resume) resume=true ;;
      *) install_fail "unknown option '$1'"; return 2 ;;
    esac
    shift
  done
  if [ -n "$requested" ] && ! [[ "$requested" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]]; then
    install_fail "expected a canonical stable or RC tag"
    return 2
  fi
  if ! { exec 3<> /dev/tty; } 2>/dev/null; then
    install_fail "an interactive terminal is required. Download and inspect the template for the manual path."
    return 1
  fi
  install_require_tools
  # The release assembly supplies an approved publisher before the script is distributed.
  publisher=${shiftbox_release_publisher:-}
  if ! [[ "$publisher" =~ ^[0-9]{12}$ ]]; then
    install_fail "use the installer attached to an official public release"
    return 1
  fi
  if ! declare -F manifest_validate >/dev/null; then
    install_fail "the release installer is missing its manifest validator"
    return 1
  fi
  if [ "$resume" = true ]; then
    if [ ! -d "$directory" ] || [ -L "$directory" ] || [ ! -O "$directory" ] ||
        [ ! -f "$directory/state.json" ] || [ -L "$directory/state.json" ]; then
      install_fail "no owned installer state is available to resume"
      return 1
    fi
  else
    if [ -e "$directory" ] || [ -L "$directory" ]; then
      install_fail "choose a new work directory or use --resume"
      return 1
    fi
    mkdir -p -- "$(dirname -- "$directory")"
    mkdir -m 700 -- "$directory"
  fi
  directory=$(cd -- "$directory" && pwd -P)
  local state="$directory/state.json" manifest="$directory/release/distribution-manifest.json"
  if ! mkdir -- "$directory/.install-lock"; then
    install_fail "another run owns this work directory. For a stale lock, verify the previous process exited before removing .install-lock."
    return 1
  fi
  scratch=''
  trap 'install_cleanup "$directory" "$scratch" "$resume"' EXIT
  scratch=$(mktemp -d "$directory/run.XXXXXXXX")

  if [ "$resume" = true ]; then
    account=$(jq -er '.account' <"$state")
    region=$(jq -er '.region' <"$state")
    release=$(jq -er '.release' <"$state")
    if [ -n "$requested" ] && [ "$requested" != "$release" ]; then
      install_fail "the requested release differs from this installation. Use the documented upgrade procedure."
      return 1
    fi
    if [ "$(manifest_sha256 "$manifest")" != "$(jq -er '.manifestSha256' <"$state")" ]; then
      install_fail "the pinned release manifest changed"; return 1
    fi
    manifest_validate "$manifest" "$release" "$(install_tag_commit "$release" "$scratch")" "$publisher"
    manifest_verify_assets "$manifest" "$directory/release"
  else
    install_prompt 'Enter the expected AWS account ID:'
    account=$REPLY
    install_prompt 'Choose us-east-1, us-east-2, us-west-1 or us-west-2:'
    region=$REPLY
    case "$region" in us-east-1|us-east-2|us-west-1|us-west-2) ;; *) install_fail "unsupported region"; return 1 ;; esac
    if ! [[ "$account" =~ ^[0-9]{12}$ ]] || [ "$account" = "$publisher" ]; then
      install_fail "choose a valid customer account"; return 1
    fi
    install_current_account "$account" "$region"
    mkdir -- "$directory/release"
    release=$(install_artifacts "$requested" "$region" "$publisher" "$directory/release")
    jq -n --arg account "$account" --arg region "$region" --arg release "$release" \
      --arg url "$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' <"$manifest")" \
      --arg sha "$(manifest_sha256 "$manifest")" '
      {account:$account,region:$region,release:$release,templateUrl:$url,manifestSha256:$sha,stack:""}
    ' >"$scratch/state.json"
    install_prompt_parameters "$scratch/state.json" "$region"
    mv -- "$scratch/state.json" "$state"
  fi
  install_current_account "$account" "$region"
  install_verify_aws_images "$manifest" "$region"
  manifest_verify_controller "$(jq -er '.controller.repository' <"$manifest")" \
    "$(jq -er '.controller.digest' <"$manifest")"
  local expected_url
  expected_url=$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' <"$manifest")
  [ "$expected_url" = "$(jq -er '.templateUrl' <"$state")" ]
  install_fetch "$expected_url" "$scratch/template.yaml"
  if ! cmp -s "$scratch/template.yaml" "$directory/release/shiftbox-aws-cloudformation.yaml"; then
    install_fail "the hosted template differs from the inspected release"; return 1
  fi
  printf 'Verified %s in %s. Inspect %s/release/shiftbox-aws-cloudformation.yaml.\n' "$release" "$region" "$directory"
  install_run_stages "$state" "$directory" "$scratch"
)

if [ -z "${BASH_SOURCE[0]:-}" ] || [ "${BASH_SOURCE[0]}" = "$0" ]; then
  install_aws "$@"
fi
}
