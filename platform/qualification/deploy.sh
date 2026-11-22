#!/usr/bin/env bash
# Validate an exact RC and run disposable infrastructure checks in the test account.
set -euo pipefail
umask 077
export AWS_PAGER='' AWS_CLI_AUTO_PROMPT=off TF_IN_AUTOMATION=1 TF_INPUT=0
export AWS_RETRY_MODE=standard AWS_MAX_ATTEMPTS=3
export AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=platform/release/manifest.sh
source "$here/../release/manifest.sh"

account=${SHIFTBOX_QUALIFICATION_ACCOUNT_ID:-}
publisher=${SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID:-}
demo=524941137496
ohio=us-east-2
regions=(us-east-1 us-west-1 us-west-2 "$ohio")

fail() {
  printf 'ShiftBox qualification: %s\n' "$*" >&2
  exit 1
}

[[ $# = 4 ]] || fail 'usage: deploy.sh manifest asset-directory sbq-RUNID-ATTEMPT evidence-directory'
manifest=$1
assets=$2
run=$3
evidence=$4
role=${SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN:-}
staffed=${SHIFTBOX_QUALIFICATION_STAFFED:-false}

[[ $run =~ ^sbq-[1-9][0-9]{0,15}-[1-9][0-9]{0,3}$ ]] || fail 'invalid qualification run identity'
[[ $staffed == true || $staffed == false ]] || fail 'invalid qualification operator mode'
[[ $account =~ ^[0-9]{12}$ && $publisher =~ ^[0-9]{12}$ &&
  $account != "$publisher" && $account != "$demo" && $publisher != "$demo" ]] ||
  fail 'expected separate approved qualification and publisher accounts'
[[ $role == "arn:aws:iam::$account:role/shiftbox-qualification-cloudformation" ]] ||
  fail 'expected a dedicated testing-account qualification service role'
[[ -d $assets && -f $manifest && -d $evidence && ! -e $evidence/automated.json ]] ||
  fail 'verified assets and a fresh private evidence directory are required'

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
template="$assets/shiftbox-aws-cloudformation.yaml"
change_name="$run-foundation"

cloud() {
  local region=$1
  shift
  # API output needed for evidence goes to files. Errors stay off public workflow logs.
  aws --region "$region" "$@" 2>"$scratch/aws-error"
}

fence() {
  local actual
  actual=$(cloud "$1" sts get-caller-identity --query Account --output text) ||
    fail 'cannot verify the qualification AWS session'
  [[ $actual == "$account" ]] || fail 'AWS credentials are outside the dedicated testing account'
}

resource_arn() {
  local id=$1 prefix=$2 suffix
  [[ $id == "$prefix"* ]] || return 1
  suffix=${id#"$prefix"}
  [[ $suffix =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
}

# jq evaluates this filter with its own run-name variable.
# shellcheck disable=SC2016
owned_tags='
  def owned($run):
    any(.[]; .Key == "project" and .Value == "shiftbox") and
    any(.[]; .Key == "cluster_name" and .Value == $run) and
    any(.[]; .Key == "shiftbox_qualification_run" and .Value == $run);
'

choose_zone() {
  local region=$1
  cloud "$region" ec2 describe-instance-type-offerings --location-type availability-zone \
    --filters 'Name=instance-type,Values=t3.medium,t4g.micro' --output json |
    jq -er --arg region "$region" '
      [.InstanceTypeOfferings[] | select(.Location | test("^" + $region + "[a-z]$"))] |
      group_by(.Location) |
      map(select(([.[].InstanceType] | unique) == ["t3.medium","t4g.micro"])) | .[0][0].Location
    '
}

review() {
  local region=$1 directory=$2 url zone stack change
  url=$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' "$manifest")
  zone=$(choose_zone "$region") || fail "could not verify an eligible availability zone in $region"
  printf '%s\n' "$zone" >"$directory/zone"
  cloud "$region" cloudformation validate-template --template-url "$url" >/dev/null ||
    fail "CloudFormation could not validate the hosted template in $region"
  if cloud "$region" cloudformation describe-stacks --stack-name "$run" >/dev/null; then
    fail "a stack already uses the qualification run name in $region"
  fi
  grep -Eq 'ValidationError.*Stack .* does not exist' "$scratch/aws-error" ||
    fail "cannot establish that the qualification stack name is unused in $region"
  jq -n --arg name "$run" --arg dns "$run.internal" --arg zone "$zone" '
    {DeploymentName:$name,DnsSuffix:$dns,AvailabilityZone:$zone,Phase:"foundation",AccessMode:"tailscale",
     CreateNatGateway:"true",DataVolumeSizeGiB:"32",InitializeDataVolume:"true"} |
    to_entries | map({ParameterKey:.key,ParameterValue:.value})
  ' >"$directory/parameters.json"
  fence "$region"
  cloud "$region" cloudformation create-change-set --stack-name "$run" \
    --change-set-type CREATE --change-set-name "$change_name" --client-token "$change_name" \
    --role-arn "$role" --capabilities CAPABILITY_IAM --template-url "$url" \
    --parameters "file://$directory/parameters.json" --tags "Key=project,Value=shiftbox" \
    "Key=cluster_name,Value=$run" "Key=shiftbox_qualification_run,Value=$run" --output json >"$directory/created.json" ||
    fail "could not create the regional change set in $region"
  stack=$(jq -er '.StackId' "$directory/created.json")
  change=$(jq -er '.Id' "$directory/created.json")
  if ! resource_arn "$stack" "arn:aws:cloudformation:$region:$account:stack/$run/" ||
    ! resource_arn "$change" "arn:aws:cloudformation:$region:$account:changeSet/$change_name/"; then
    fail "CloudFormation returned an unexpected stack or change-set identity in $region"
  fi
  cloud "$region" cloudformation wait change-set-create-complete --change-set-name "$change" ||
    fail "the change set did not validate in $region"
  cloud "$region" cloudformation describe-change-set --change-set-name "$change" --output json >"$directory/review.json" ||
    fail "the change set could not be inspected in $region"
  jq -e --arg stack "$stack" --arg change "$change" --arg name "$change_name" --arg run "$run" \
    --slurpfile expected "$directory/parameters.json" "$owned_tags"'
    .StackId == $stack and .ChangeSetId == $change and .ChangeSetName == $name and
    .Status == "CREATE_COMPLETE" and .ExecutionStatus == "AVAILABLE" and .Capabilities == ["CAPABILITY_IAM"] and
    (.Tags | owned($run)) and
    (.Parameters as $parameters | all($expected[0][]; . as $target |
      any($parameters[]; .ParameterKey == $target.ParameterKey and .ParameterValue == $target.ParameterValue))) and
    (.Changes | type == "array" and length > 0 and
      any(.[]; .ResourceChange.LogicalResourceId == "InstallationVolume" and
        .ResourceChange.ResourceType == "AWS::EC2::Volume") and
      all(.[]; .Type == "Resource" and (.ResourceChange |
        .Action == "Add" and (.ResourceType | test("^AWS::(EC2|ECS|IAM|Logs|Route53|SecretsManager|ServiceDiscovery)::")) and
        (.ResourceType != "AWS::ECS::Service" and .ResourceType != "AWS::ECS::TaskDefinition") and
        (.LogicalResourceId | startswith("Router") or . == "TailscaleRouter" | not))))
  ' "$directory/review.json" >/dev/null ||
    fail "change-set parameters, ownership or foundation changes are invalid in $region"
  cloud "$region" cloudformation get-template --stack-name "$stack" --change-set-name "$change" \
    --template-stage Original --output json | jq -ej '.TemplateBody | strings' >"$directory/observed-template.yaml" ||
    fail "the hosted change-set template could not be read in $region"
  cmp -s "$template" "$directory/observed-template.yaml" ||
    fail "observed CloudFormation template differs from the verified RC in $region"
  cloud "$region" cloudformation describe-events --change-set-name "$change" --output json >"$directory/events.json" ||
    fail "could not read pre-deployment validation events in $region"
  jq -e '.OperationEvents | type == "array" and all(.[];
    .EventType != "VALIDATION_ERROR" and .ValidationStatus != "FAILED" and .ValidationStatus != "SKIPPED")
  ' "$directory/events.json" >/dev/null || fail "the hosted template has validation findings in $region"

  if [[ $region != "$ohio" || $staffed == true ]]; then
    fence "$region"
    cloud "$region" cloudformation delete-change-set --change-set-name "$change" ||
      fail "could not discard the unexecuted change set in $region"
    cloud "$region" cloudformation delete-stack --stack-name "$stack" ||
      fail "could not discard the unexecuted review stack in $region"
    cloud "$region" cloudformation wait stack-delete-complete --stack-name "$stack" ||
      fail "could not verify deletion of the review stack in $region"
  fi
}

inspect_ami() {
  local region=$1 pin=$2 ami
  ami=$(jq -er '.amiId' "$pin")
  cloud "$region" ec2 describe-images --image-ids "$ami" --owners "$publisher" --output json |
    jq -e --slurpfile pin "$pin" --arg owner "$publisher" --arg now "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '
      $pin[0] as $w |
      .Images | length == 1 and (.[0] |
        .ImageId == $w.amiId and .OwnerId == $owner and .Public == true and .State == "available" and
        .Architecture == "x86_64" and .RootDeviceType == "ebs" and .VirtualizationType == "hvm" and
        .RootDeviceName == $w.rootDeviceName and
        ((.DeprecationTime // "") | sub("\\.[0-9]+Z$";"Z") | . == "" or . > $now) and
        ([.BlockDeviceMappings[] | select(.DeviceName == $w.rootDeviceName)] |
          length == 1 and (.[0].Ebs |
            .SnapshotId == $w.rootSnapshotId and .VolumeSize == $w.minimumDiskGiB and .Encrypted != true)))
    ' >/dev/null || fail "the exact public workspace AMI failed provenance checks in $region"
}

inspect_host_image() {
  local region=$1 host
  host=$(jq -er --arg region "$region" '.regions[$region].hostAmiId' "$manifest")
  cloud "$region" ec2 describe-images --image-ids "$host" --owners amazon --output json |
    jq -e --arg host "$host" '
      .Images | length == 1 and (.[0] |
        .ImageId == $host and .State == "available" and .Architecture == "x86_64" and
        .RootDeviceType == "ebs" and .VirtualizationType == "hvm")
    ' >/dev/null || fail "the exact ECS host AMI failed verification in $region"
}

tf() {
  local directory=$1
  shift
  terraform -chdir="$directory" "$@" >>"$directory/terraform.log" 2>&1
}

destroy_probe() {
  local region=$1 directory=$2
  fence "$region"
  tf "$directory" plan -input=false -no-color -destroy -out=destroy.tfplan &&
    tf "$directory" apply -input=false -no-color destroy.tfplan
}

probe() (
  local region=$1 directory=$2 instance disk ami
  fence "$region"
  cp "$here/../terraform/publisher/tests/public-ami-launch/"{main.tf,.terraform.lock.hcl} "$directory/"
  ami=$(jq -er '.amiId' "$directory/pin.json")
  jq -n --arg account "$account" --arg publisher "$publisher" --arg region "$region" \
    --arg zone "$(cat "$directory/zone")" --arg ami "$ami" --arg name "$run-p" '
    {customer_account_id:$account,publisher_account_id:$publisher,aws_region:$region,
     availability_zone:$zone,ami_id:$ami,name:$name}
  ' >"$directory/probe.auto.tfvars.json"
  tf "$directory" init -input=false -no-color -lockfile=readonly || fail "Terraform probe initialization failed in $region"
  tf "$directory" plan -input=false -no-color -out=launch.tfplan || fail "Terraform probe planning failed in $region"
  # Once apply starts, always try the probe's own destroy plan before the runner
  # returns. An independent sweep owns orphan recovery when runner state is lost.
  trap 'destroy_probe "$region" "$directory" || printf "Presence of probe resources is uncertain in %s\n" "$region" >&2' EXIT
  fence "$region"
  tf "$directory" apply -input=false -no-color launch.tfplan || fail "Terraform probe launch failed in $region"
  instance=$(terraform -chdir="$directory" output -raw instance_id 2>>"$directory/terraform.log")
  disk=$(terraform -chdir="$directory" output -raw root_volume_id 2>>"$directory/terraform.log")
  [[ $instance =~ ^i-[0-9a-f]{17}$ && $disk =~ ^vol-[0-9a-f]{17}$ ]] ||
    fail "Terraform returned unexpected probe resource identities in $region"
  cloud "$region" ec2 wait instance-status-ok --instance-ids "$instance" ||
    fail "the probe instance did not pass EC2 status checks in $region"
  cloud "$region" ec2 describe-instances --instance-ids "$instance" --output json |
    jq -e --arg account "$account" --arg ami "$ami" --arg id "$instance" --arg disk "$disk" \
      --arg cluster "$run-p" --slurpfile pin "$directory/pin.json" '
      $pin[0].rootDeviceName as $root |
      .Reservations | length == 1 and (.[0] | .OwnerId == $account and
        (.Instances | length == 1 and (.[0] |
          .InstanceId == $id and .ImageId == $ami and .State.Name == "running" and .RootDeviceName == $root and
          .MetadataOptions.HttpTokens == "required" and (.PublicIpAddress == null) and (.IamInstanceProfile == null) and
          all(.NetworkInterfaces[]?; .Association.PublicIp == null) and
          any(.Tags[]; .Key == "project" and .Value == "shiftbox") and
          any(.Tags[]; .Key == "cluster_name" and .Value == $cluster) and
          any(.BlockDeviceMappings[]; .DeviceName == $root and .Ebs.VolumeId == $disk and .Ebs.DeleteOnTermination == true))))
    ' >/dev/null || fail "the booted probe instance differs from the isolated plan in $region"
  cloud "$region" ec2 describe-volumes --volume-ids "$disk" --output json |
    jq -e --arg id "$instance" --arg disk "$disk" --slurpfile pin "$directory/pin.json" '
      $pin[0] as $w |
      .Volumes | length == 1 and (.[0] |
        .VolumeId == $disk and .Encrypted == true and .VolumeType == "gp3" and .SnapshotId == $w.rootSnapshotId and
        (.Attachments | length == 1 and (.[0] | .InstanceId == $id and .Device == $w.rootDeviceName and .State == "attached")))
    ' >/dev/null || fail "the probe did not use the exact encrypted AMI root in $region"
  destroy_probe "$region" "$directory" || fail "Terraform could not destroy the probe in $region"
  trap - EXIT
  jq -n --arg region "$region" --arg ami "$ami" '
    {region:$region,amiId:$ami,amiBooted:true,hostedChangeSetValidated:true,terraformDestroyCompleted:true}
  ' >"$directory/result.json"
)

start_foundation() {
  local directory="$scratch/$ohio" stack change volume
  stack=$(jq -er '.StackId' "$directory/review.json")
  change=$(jq -er '.ChangeSetId' "$directory/review.json")
  fence "$ohio"
  cloud "$ohio" cloudformation describe-change-set --change-set-name "$change" --output json >"$directory/recheck.json" ||
    fail 'the Ohio change set could not be rechecked before execution'
  diff -q <(jq -cS . "$directory/review.json") <(jq -cS . "$directory/recheck.json") >/dev/null ||
    fail 'the Ohio change set changed after validation'
  cloud "$ohio" cloudformation execute-change-set --change-set-name "$change" --client-request-token "$change_name" ||
    fail 'could not execute the reviewed Ohio foundation'
  cloud "$ohio" cloudformation wait stack-create-complete --stack-name "$stack" ||
    fail 'the Ohio foundation did not finish creating'
  fence "$ohio"
  cloud "$ohio" cloudformation set-stack-policy --stack-name "$stack" \
    --stack-policy-body "file://$scratch/stack-policy.json" ||
    fail 'could not install the verified storage protection policy'
  cloud "$ohio" cloudformation get-stack-policy --stack-name "$stack" --output json |
    jq -e --slurpfile expected "$scratch/stack-policy.json" '.StackPolicyBody | fromjson == $expected[0]' >/dev/null ||
    fail 'the installed Ohio stack policy differs from the approved release'
  cloud "$ohio" cloudformation update-termination-protection --enable-termination-protection --stack-name "$stack" >/dev/null ||
    fail 'could not protect the Ohio foundation from accidental deletion'
  cloud "$ohio" cloudformation describe-stacks --stack-name "$stack" --output json >"$directory/deployed.json" ||
    fail 'could not inspect the deployed Ohio foundation'
  jq -e --arg stack "$stack" --arg run "$run" --arg role "$role" "$owned_tags"'
    .Stacks | length == 1 and (.[0] |
      .StackId == $stack and .StackName == $run and .StackStatus == "CREATE_COMPLETE" and .RoleARN == $role and
      .EnableTerminationProtection == true and (.Tags | owned($run)) and
      any(.Outputs[]; .OutputKey == "Phase" and .OutputValue == "foundation"))
  ' "$directory/deployed.json" >/dev/null || fail 'Ohio foundation ownership, phase or protection verification failed'
  volume=$(jq -er '.Stacks[0].Outputs[] | select(.OutputKey == "InstallationVolumeId") | .OutputValue |
    select(test("^vol-[0-9a-f]{17}$"))' "$directory/deployed.json")
  cloud "$ohio" cloudformation list-stack-resources --stack-name "$stack" --output json |
    jq -e --arg volume "$volume" '
      any(.StackResourceSummaries[]; .LogicalResourceId == "InstallationVolume" and .PhysicalResourceId == $volume) and
      all(.StackResourceSummaries[]; (.LogicalResourceId |
        test("^(PostgresTask|SetupTask|ControllerTask|PostgresService|ControllerService|SetupSession|TailscaleRouter)$") | not))
    ' >/dev/null || fail 'the Ohio foundation unexpectedly started a protected setup or active resource'
  cloud "$ohio" ec2 describe-volumes --volume-ids "$volume" --output json |
    jq -e --arg volume "$volume" '.Volumes | length == 1 and
      (.[0] | .VolumeId == $volume and .Encrypted == true and .VolumeType == "gp3")' >/dev/null ||
    fail 'could not verify encrypted Ohio foundation storage'
}

fence "$ohio"
jq -es --arg publisher "$publisher" "$manifest_filter"'
  length == 1 and (.[0] | distribution and .publisherAccountId == $publisher)
' "$manifest" >/dev/null || fail 'expected the approved public publisher and a complete exact RC manifest'
manifest_verify_assets "$manifest" "$assets" || fail 'RC asset verification failed'
"$here/../release/templates.sh" verify "$manifest" "$template" || fail 'hosted template bytes differ in a release region'
tar -xOzf "$assets/shiftbox-aws-support.tar.gz" stack-policy.json >"$scratch/stack-policy.json"
jq -e '.Statement | type == "array"' "$scratch/stack-policy.json" >/dev/null ||
  fail 'the verified RC is missing its storage protection policy'

for region in "${regions[@]}"; do
  mkdir "$scratch/$region"
  jq -er --arg region "$region" '.regions[$region].workspaceImage' "$manifest" >"$scratch/$region/pin.json"
  inspect_ami "$region" "$scratch/$region/pin.json"
  inspect_host_image "$region"
done
for region in "${regions[@]}"; do
  review "$region" "$scratch/$region"
done
for region in "${regions[@]}"; do
  probe "$region" "$scratch/$region"
done
status='ohio-console-launch-pending'
if [[ $staffed == false ]]; then
  start_foundation
  status='automated-foundation-only'
fi

jq -s --arg candidate "$(jq -er .candidateTag "$manifest")" --arg sha "$(manifest_sha256 "$manifest")" \
  --arg status "$status" '
  {schemaVersion:1,candidateTag:$candidate,manifestSha256:$sha,status:$status,
   ohioFoundationDeployed:($status == "automated-foundation-only"),manualGatesPending:true,regions:sort_by(.region)}
' "$scratch/"*/result.json >"$evidence/automated.json"
printf 'Automated checks passed (%s). Browser, Tailscale, CLI, recovery and teardown evidence remain separate.\n' "$status"
