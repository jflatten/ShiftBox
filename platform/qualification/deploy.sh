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

verify_staffed=false
if [[ ${1:-} == --verify-staffed-dns ]]; then
  verify_staffed=true
  shift
fi
[[ $# = 4 ]] || fail 'usage: deploy.sh [--verify-staffed-dns] manifest asset-directory sbq-RUNID-ATTEMPT evidence-directory'
manifest=$1
assets=$2
run=$3
evidence=$4
role=${SHIFTBOX_QUALIFICATION_SERVICE_ROLE_ARN:-}
staffed=${SHIFTBOX_QUALIFICATION_STAFFED:-false}
staffed_role=${SHIFTBOX_QUALIFICATION_STAFFED_SERVICE_ROLE_ARN:-}

[[ $run =~ ^sbq-[1-9][0-9]{0,15}-[1-9][0-9]{0,3}$ ]] || fail 'invalid qualification run identity'
[[ $staffed == true || $staffed == false ]] || fail 'invalid qualification operator mode'
[[ $account =~ ^[0-9]{12}$ && $publisher =~ ^[0-9]{12}$ &&
  $account != "$publisher" && $account != "$demo" && $publisher != "$demo" ]] ||
  fail 'expected separate approved qualification and publisher accounts'
[[ $role == "arn:aws:iam::$account:role/shiftbox-qualification-cloudformation" ]] ||
  fail 'expected a dedicated testing-account qualification service role'
if [[ $staffed == true && $staffed_role != "arn:aws:iam::$account:role/shiftbox-qualification-staffed-cloudformation" ]]; then
  fail 'a staffed run requires the separately reviewed testing-account service role for the console and teardown'
fi
[[ -d $assets && -f $manifest && -d $evidence ]] ||
  fail 'verified assets and a private evidence directory are required'
if [[ $verify_staffed == true ]]; then
  [[ $staffed == true && -f $evidence/automated.json ]] || fail 'an existing staffed handoff is required'
else
  [[ ! -e $evidence/automated.json ]] || fail 'a fresh private evidence directory is required'
fi

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
template="$assets/shiftbox-aws-cloudformation.yaml"

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

select_network() {
  local region=$1 directory=$2 service workspace zone
  local configured=${SHIFTBOX_QUALIFICATION_SUBNETS:-}
  service=$(jq -er --arg region "$region" '.[$region].service | strings | select(test("^subnet-[0-9a-f]{17}$"))' <<<"$configured") ||
    fail "configure the existing service subnet for $region"
  workspace=$(jq -er --arg region "$region" '.[$region].workspace | strings | select(test("^subnet-[0-9a-f]{17}$"))' <<<"$configured") ||
    fail "configure the existing workspace subnet for $region"
  cloud "$region" ec2 describe-subnets --subnet-ids "$service" "$workspace" --output json |
    jq -ce --arg account "$account" --arg region "$region" --arg service "$service" --arg workspace "$workspace" '
      def selected($id):
        [.Subnets[] | select(.SubnetId == $id and .OwnerId == $account and
          .State == "available" and .MapPublicIpOnLaunch == false)] |
        select(length == 1) | .[0];
      selected($service) as $host | selected($workspace) as $guest |
      select($host.VpcId == $guest.VpcId and ($host.VpcId | test("^vpc-[0-9a-f]{17}$")) and
        ($host.AvailabilityZone | test("^" + $region + "[a-z]$"))) |
      {VpcId:$host.VpcId,ServiceSubnet:$service,WorkspaceSubnet:$workspace,AvailabilityZone:$host.AvailabilityZone}
    ' >"$directory/network.json" || fail "qualification requires existing private subnets in the same testing-account VPC in $region"
  zone=$(jq -er .AvailabilityZone "$directory/network.json")
  cloud "$region" ec2 describe-instance-type-offerings --location-type availability-zone \
    --filters 'Name=instance-type,Values=t3.medium' --output json |
    jq -e --arg zone "$zone" 'any(.InstanceTypeOfferings[]; .Location == $zone and .InstanceType == "t3.medium")' >/dev/null ||
    fail "the service subnet's Availability Zone is not eligible in $region"
  printf '%s\n' "$zone" >"$directory/zone"
}

review_change_set() {
  local region=$1 directory=$2 phase=$3 type=$4 target=$5
  local url stack change change_name="$run-$phase"
  url=$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' "$manifest")
  jq -n --arg name "$run" --arg dns "$run.internal" --arg phase "$phase" \
    --slurpfile selected "$scratch/$region/network.json" '
    ($selected[0] + {DeploymentName:$name,DnsSuffix:$dns,Phase:$phase,OperatorCidr:"",
     RoleBoundaryMode:"external",DataVolumeSizeGiB:"32",InitializeDataVolume:"true"}) |
    to_entries | map({ParameterKey:.key,ParameterValue:.value})
  ' >"$directory/parameters.json"
  fence "$region"
  cloud "$region" cloudformation create-change-set --stack-name "$target" \
    --change-set-type "$type" --change-set-name "$change_name" --client-token "$change_name" \
    --role-arn "$role" --capabilities CAPABILITY_NAMED_IAM --template-url "$url" \
    --parameters "file://$directory/parameters.json" --tags "Key=project,Value=shiftbox" \
    "Key=cluster_name,Value=$run" "Key=shiftbox_qualification_run,Value=$run" --output json >"$directory/created.json" ||
    fail "could not create the $phase change set in $region"
  stack=$(jq -er '.StackId' "$directory/created.json")
  change=$(jq -er '.Id' "$directory/created.json")
  if ! resource_arn "$stack" "arn:aws:cloudformation:$region:$account:stack/$run/" ||
    ! resource_arn "$change" "arn:aws:cloudformation:$region:$account:changeSet/$change_name/" ||
    [[ $type == UPDATE && $stack != "$target" ]]; then
    fail "CloudFormation returned an unexpected stack or change-set identity in $region"
  fi
  cloud "$region" cloudformation wait change-set-create-complete --change-set-name "$change" ||
    fail "the change set did not validate in $region"
  cloud "$region" cloudformation describe-change-set --change-set-name "$change" --output json >"$directory/review.json" ||
    fail "the change set could not be inspected in $region"
  jq -e --arg stack "$stack" --arg change "$change" --arg name "$change_name" --arg run "$run" --arg phase "$phase" \
    --slurpfile expected "$directory/parameters.json" "$owned_tags"'
    def planned:
      .Changes | type == "array" and length > 0 and
      all(.[]; .Type == "Resource" and .ResourceChange.Action == "Add");
    .StackId == $stack and .ChangeSetId == $change and .ChangeSetName == $name and
    .Status == "CREATE_COMPLETE" and .ExecutionStatus == "AVAILABLE" and .Capabilities == ["CAPABILITY_NAMED_IAM"] and
    (.Tags | owned($run)) and
    (.Parameters as $parameters | all($expected[0][]; . as $target |
      any($parameters[]; .ParameterKey == $target.ParameterKey and .ParameterValue == $target.ParameterValue))) and
    planned and
    (if $phase == "dns-zones" then
      ([.Changes[].ResourceChange | [.LogicalResourceId, .ResourceType]] | sort) ==
        [["DiscoveryNamespace", "AWS::ServiceDiscovery::PrivateDnsNamespace"], ["PrivateZone", "AWS::Route53::HostedZone"]]
    else .Changes |
      any(.[]; .ResourceChange.LogicalResourceId == "InstallationVolume" and
        .ResourceChange.ResourceType == "AWS::EC2::Volume") and
      any(.[]; .ResourceChange.LogicalResourceId == "HostRole" and
        .ResourceChange.ResourceType == "AWS::IAM::Role") and
      all(.[]; .ResourceChange |
        (.ResourceType | test("^AWS::(EC2|SecretsManager)::|^AWS::(ECS::Cluster|IAM::Role|IAM::InstanceProfile|Logs::LogGroup)$")) and
        (if (.ResourceType | startswith("AWS::IAM::")) then
          .LogicalResourceId == "HostRole" or .LogicalResourceId == "HostProfile"
        else true end) and
        (.ResourceType | IN("AWS::EC2::VPC","AWS::EC2::Subnet","AWS::EC2::RouteTable","AWS::EC2::Route",
          "AWS::EC2::SubnetRouteTableAssociation","AWS::EC2::InternetGateway","AWS::EC2::VPCGatewayAttachment",
          "AWS::EC2::NatGateway","AWS::EC2::EIP") | not) and
        (.LogicalResourceId | startswith("Router") or . == "TailscaleRouter" | not))
    end)
  ' "$directory/review.json" >/dev/null ||
    fail "$phase change set parameters, ownership or resource changes are invalid in $region"
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
}

review() {
  local region=$1 directory=$2 stack change
  cloud "$region" cloudformation validate-template --template-url "$(jq -er --arg region "$region" '.regions[$region].templateLocation.url' "$manifest")" >/dev/null ||
    fail "CloudFormation could not validate the hosted template in $region"
  if cloud "$region" cloudformation describe-stacks --stack-name "$run" >/dev/null; then
    fail "a stack already uses the qualification run name in $region"
  fi
  grep -Eq 'ValidationError.*Stack .* does not exist' "$scratch/aws-error" ||
    fail "cannot establish that the qualification stack name is unused in $region"
  review_change_set "$region" "$directory" foundation CREATE "$run"
  if [[ $region != "$ohio" || $staffed == true ]]; then
    stack=$(jq -er '.StackId' "$directory/review.json")
    change=$(jq -er '.ChangeSetId' "$directory/review.json")
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
  cloud "$ohio" cloudformation execute-change-set --change-set-name "$change" --client-request-token "$run-foundation" ||
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
  jq -e --slurpfile selected "$directory/network.json" '
    .Stacks[0].Outputs as $outputs |
    def output($key): [$outputs[] | select(.OutputKey == $key) | .OutputValue];
    $selected[0] as $network |
    output("VpcId") == [$network.VpcId] and output("ServiceSubnetId") == [$network.ServiceSubnet] and
      output("WorkspaceSubnetId") == [$network.WorkspaceSubnet]
  ' "$directory/deployed.json" >/dev/null || fail 'the Ohio foundation selected other network inputs'
  cloud "$ohio" cloudformation list-stack-resources --stack-name "$stack" --output json |
    jq -e --arg volume "$volume" '
      any(.StackResourceSummaries[]; .LogicalResourceId == "InstallationVolume" and .PhysicalResourceId == $volume) and
      all(.StackResourceSummaries[]; .ResourceType != "AWS::EC2::VPC" and .ResourceType != "AWS::EC2::Subnet" and
        (.LogicalResourceId |
        test("^(PostgresTask|SetupTask|ControllerTask|PostgresService|ControllerService|SetupSession|TailscaleRouter|PrivateZone|DiscoveryNamespace|PostgresDiscovery|ControllerDiscovery|PrivateRecords|PostgresExecutionRole|SetupExecutionRole|ControllerExecutionRole|SetupTaskRole|ControllerTaskRole)$") | not))
    ' >/dev/null || fail 'the Ohio foundation unexpectedly started a protected setup or active resource'
  cloud "$ohio" ec2 describe-volumes --volume-ids "$volume" --output json |
    jq -e --arg volume "$volume" '.Volumes | length == 1 and
      (.[0] | .VolumeId == $volume and .Encrypted == true and .VolumeType == "gp3")' >/dev/null ||
    fail 'could not verify encrypted Ohio foundation storage'

  # Cloud Map assigns its physical namespace and service IDs on creation. The
  # execution role must not tag arbitrary IDs, so only review this first DNS stage.
  mkdir "$directory/dns-zones"
  review_change_set "$ohio" "$directory/dns-zones" dns-zones UPDATE "$stack"
  change=$(jq -er '.ChangeSetId' "$directory/dns-zones/review.json")
  fence "$ohio"
  cloud "$ohio" cloudformation delete-change-set --change-set-name "$change" ||
    fail 'could not discard the unexecuted Ohio DNS zone change set'
}

verify_staffed_dns() {
  local stack receipt
  jq -e --arg sha "$(manifest_sha256 "$manifest")" '
    .status == "ohio-console-launch-pending" and .manifestSha256 == $sha and
    .manualGatesPending == true and .dnsBootstrapExecuted == false
  ' "$evidence/automated.json" >/dev/null || fail 'the staffed handoff does not identify this candidate'
  cloud "$ohio" cloudformation describe-stacks --stack-name "$run" --output json >"$scratch/staffed-stack.json" ||
    fail 'the staffed Ohio stack is unavailable for DNS readback'
  stack=$(jq -er --arg run "$run" --arg role "$staffed_role" --slurpfile selected "$evidence/.network.json" "$owned_tags"'
    def parameter($key): [.Parameters[] | select(.ParameterKey == $key) | .ParameterValue];
    .Stacks | select(length == 1) | .[0] | . as $stack_parameters |
    select(.StackName == $run and .StackStatus == "UPDATE_COMPLETE" and .RoleARN == $role and
      (.Capabilities | index("CAPABILITY_NAMED_IAM") != null) and .EnableTerminationProtection == true and
      (.Tags | owned($run)) and parameter("RoleBoundaryMode") == ["managed"] and
      parameter("DnsSuffix") == [($run + ".internal")] and
      all($selected[0] | to_entries[]; . as $item |
        [$item.value] == ($stack_parameters | parameter($item.key)))) | .StackId
  ' "$scratch/staffed-stack.json") ||
    fail 'the staffed Ohio stack does not match the reviewed role, parameters or ownership'
  resource_arn "$stack" "arn:aws:cloudformation:$ohio:$account:stack/$run/" ||
    fail 'the staffed stack is outside this qualification run'
  cloud "$ohio" cloudformation get-template --stack-name "$stack" --template-stage Original --output json |
    jq -ej '.TemplateBody | strings' >"$scratch/staffed-template.yaml" ||
    fail 'could not read the template used by the staffed stack'
  cmp -s "$template" "$scratch/staffed-template.yaml" ||
    fail 'the staffed stack template differs from the exact RC'
  cloud "$ohio" cloudformation list-stack-resources --stack-name "$stack" --output json |
    jq -e --slurpfile stack "$scratch/staffed-stack.json" --slurpfile selected "$evidence/.network.json" '
      def zone_id: ltrimstr("/hostedzone/");
      def output($key; $pattern):
        [$stack[0].Stacks[0].Outputs[] | select(.OutputKey == $key) | .OutputValue] |
        if length == 1 and (.[0] | type == "string" and test($pattern)) then .[0]
        else error("missing or invalid staffed DNS output") end;
      def resource($key; $resource_type):
        [.StackResourceSummaries[] | select(.LogicalResourceId == $key and .ResourceType == $resource_type and
          (.ResourceStatus | IN("CREATE_COMPLETE", "UPDATE_COMPLETE"))) | .PhysicalResourceId] |
        if length == 1 and (.[0] | type == "string") and .[0] != "" then .[0]
        else error("missing staffed DNS resource \($key)") end;
      "^(/hostedzone/)?Z[A-Z0-9]{5,32}$" as $zone |
      (output("PrivateZoneId"; $zone) | zone_id) as $private |
      (output("DiscoveryHostedZoneId"; $zone) | zone_id) as $discovery |
      output("DiscoveryNamespaceId"; "^ns-[a-z0-9]{16}$") as $namespace |
      output("PostgresDiscoveryServiceId"; "^srv-[a-z0-9]{16}$") as $postgres |
      output("ControllerDiscoveryServiceId"; "^srv-[a-z0-9]{16}$") as $controller |
      $private != $discovery and $postgres != $controller and
      all(.StackResourceSummaries[]; .ResourceType != "AWS::EC2::VPC" and .ResourceType != "AWS::EC2::Subnet" and
        (.LogicalResourceId | startswith("Router") or . == "TailscaleRouter" | not)) and
      (output("Phase"; "^(database|active)$") | length > 0) and
      output("VpcId"; "^vpc-[0-9a-f]{17}$") == $selected[0].VpcId and
      output("ServiceSubnetId"; "^subnet-[0-9a-f]{17}$") == $selected[0].ServiceSubnet and
      output("WorkspaceSubnetId"; "^subnet-[0-9a-f]{17}$") == $selected[0].WorkspaceSubnet and
      $private == (resource("PrivateZone"; "AWS::Route53::HostedZone") | zone_id) and
      $namespace == resource("DiscoveryNamespace"; "AWS::ServiceDiscovery::PrivateDnsNamespace") and
      $postgres == resource("PostgresDiscovery"; "AWS::ServiceDiscovery::Service") and
      $controller == resource("ControllerDiscovery"; "AWS::ServiceDiscovery::Service") and
      resource("PrivateRecords"; "AWS::Route53::RecordSetGroup") != ""
    ' >/dev/null ||
    fail 'the staffed stack does not expose the verified staged DNS inventory'
  receipt=$(mktemp "$evidence/.staffed.XXXXXX")
  jq '.staffedDnsInventoryVerified = true' "$evidence/automated.json" >"$receipt"
  mv -- "$receipt" "$evidence/automated.json"
  printf 'Verified the exact staffed stack and its DNS inventory. Operator and teardown evidence remain pending.\n'
}

fence "$ohio"
jq -es --arg publisher "$publisher" "$manifest_filter"'
  length == 1 and (.[0] | distribution and .publisherAccountId == $publisher)
' "$manifest" >/dev/null || fail 'expected the approved public publisher and a complete exact RC manifest'
manifest_verify_assets "$manifest" "$assets" || fail 'RC asset verification failed'
if [[ $verify_staffed == true ]]; then
  verify_staffed_dns
  exit 0
fi
"$here/../release/templates.sh" verify "$manifest" "$template" || fail 'hosted template bytes differ in a release region'
tar -xOzf "$assets/shiftbox-aws-support.tar.gz" stack-policy.json >"$scratch/stack-policy.json"
jq -e '.Statement | type == "array"' "$scratch/stack-policy.json" >/dev/null ||
  fail 'the verified RC is missing its storage protection policy'

for region in "${regions[@]}"; do
  mkdir "$scratch/$region"
  select_network "$region" "$scratch/$region"
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
cp -- "$scratch/$ohio/network.json" "$evidence/.network.json"
if [[ $staffed == false ]]; then
  start_foundation
  status='automated-foundation-only'
fi

jq -s --arg candidate "$(jq -er .candidateTag "$manifest")" --arg sha "$(manifest_sha256 "$manifest")" \
  --arg status "$status" '
  {schemaVersion:1,candidateTag:$candidate,manifestSha256:$sha,status:$status,
   ohioFoundationDeployed:($status == "automated-foundation-only"),
   dnsZoneChangeSetReviewed:($status == "automated-foundation-only"),
   dnsBootstrapExecuted:false,manualGatesPending:true,regions:sort_by(.region)}
' "$scratch/"*/result.json >"$evidence/automated.json"
printf 'Automated checks passed (%s). Browser, private access, CLI, recovery and teardown evidence remain separate.\n' "$status"
