# ShiftBox

ShiftBox is an open-source development sandboxing solution that utilizes the devcontainer
standard to provide a fully isolated and configured development environments that are shared between
the developer and AI development agents.

## Agent Rules

  When you are thinking and generating code, use our design decisions as your primary guidance.
  When expanding the scope or design always first confirm with a human.  Your guiding principle
  is to ask when you are unsure. Ground all decisions in our approved architecuture.  Always
  consult the top level documentation and follow relevant links to details to understand or
  approved architecture here: `documentation/architecture/design-readback.md`

  When executing a specification, only implement what the tasks request.  Refrain from over-
  engineering and expanding into edges cases that are not explicitly called out.

  Always report what task you are working on in your responses. Example:
  
  ```text
  T104: Building the backend API infrastructure endpoint /api/v1/loging
  ```

  Always checkoff tasks when they are complete immediately in `tasks.md`

  Administrative and operator-facing capabilities are complete only when discoverable admin UI
  controls and state, permissions, and validation ship with the backend and are checked end-to-end,
  including save/reload persistence; API or database support alone is not complete.

  Tasks that are labeld with `[P]` should be run in parallel inside a subagent.

  Do not run more than 5 subagent parallel tasks as one time.

  The project has the appropriate skills installed for the technologies being utilized.  Always
  look for a relevant skill before proceeding.

  Never edit or change the documents in `documentation/architecture`.  These are reference only
  documents that require human oversight.

  All development should be using the `shiftbox-demo` test instance in AWS as our shared testbed.

  Deploy changes to `shiftbox-demo` for human review using the 'default' AWS profile that provides
  access to the 524941137496 account in the us-east-2 region.

## Architecture and Guiding Principles

- Simplicity in ALL things
- Components displayed in the UI should have human readible name and not UUID's
- Human readible code with comments that help a reader understand the context of the code as it relates to the application as a whole.
- Workflows are always expressed visually in our UI in realtime using Mantine Stepper.
- Always use Mantine controls for our UI. Reference: <https://mantine.dev/llms.txt>
- Always use IBM's Carbon Design System for our UI. Reference: <https://www.carbondesignsystem.com/>
- States should always be displayed in the UI using chips. Reference: <https://mantine.dev/core/chip/>
- Always use the mantine MCP server when selecting components.
- Always apply our branding standard with the artifacts located in the `branding/` directory.

## AWS Practices

- Always use Terraform to manage AWS resources
- Always clean up your test resources when completing your task.
- Prefix new ShiftBox-managed AWS tag keys with `shiftbox-`. For new
  CloudFormation deployments use `shiftbox-project=shiftbox` and
  `shiftbox-cluster-name=<name of deployment>` where tagging is supported.
  Preserve existing tag contracts for deployed resources and legacy release assets.

## Deploy an update to the AWS demo

Use the `default` AWS profile in account `524941137496` and region `us-east-2`. The ECS cluster is
`shiftbox-demo`, with services named `controller` and `postgres`. Run these steps from the repository
root in Bash or Zsh with Docker running and the private demo network connected. Use the existing
local Terraform state in `.cache/aws-demo/terraform.tfstate` and the existing inputs in
`.cache/aws-demo/ecs-host.tfvars` and `.cache/aws-demo/deployment-current.tfvars.json`. On a new machine,
obtain those deployment files from the current operator before planning. Keep the state, variables,
plans, and registry credentials local.

### Build and publish the controller

Review `git status --short` before building. The browser export and controller binary use the source
currently on disk. These commands use the repository's cached Go and Node toolchains when present.
Run `npm --prefix ui ci` if UI dependencies are missing or the lockfile has changed.

```sh
set -eu
set -o pipefail
umask 077
repo="$(git rev-parse --show-toplevel)"
cd "$repo"
export AWS_PROFILE=default AWS_REGION=us-east-2 AWS_DEFAULT_REGION=us-east-2
export PATH="$repo/.cache/toolchains/go/bin:$repo/.cache/toolchains/node-v26.13.0-darwin-arm64/bin:$PATH"
export GOCACHE="$repo/.cache/gobuild"

demo="$repo/.cache/aws-demo"
state="$demo/terraform.tfstate"
active_vars="$demo/deployment-current.tfvars.json"
test -s "$state"
test -s "$demo/ecs-host.tfvars"
test -s "$active_vars"
test "$(aws sts get-caller-identity --query Account --output text)" = 524941137496
rollout="$(mktemp -d "$demo/rollout.XXXXXX")"
mkdir -p "$rollout/overlay" "$rollout/docker"
cp "$active_vars" "$rollout/previous.tfvars.json"

npm --prefix ui run build
origin="$(jq -er .outputs.management_origin.value "$state")"
sh platform/helper/macos/package.sh "$origin" "$rollout/helper"
install -d ui/out/_shiftbox
install -m 0644 "$rollout/helper/ShiftBox-Local-Helper.dmg" ui/out/_shiftbox/ShiftBox-Local-Helper.dmg
SHIFTBOX_REQUIRE_MACOS_HELPER=1 CGO_ENABLED=0 go test -tags shiftbox_ui ./cmd/shiftbox \
  -run '^Test(EmbeddedBrowserAssets|EmbeddedMacOSHelper)$' -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags shiftbox_ui \
  -o "$rollout/overlay/shiftbox" ./cmd/shiftbox
chmod 0755 "$rollout/overlay/shiftbox"

terraform -chdir=platform/terraform/aws init -lockfile=readonly
repository="$(terraform -chdir=platform/terraform/aws output -state="$state" -raw controller_repository_url)"
registry="${repository%%/*}"
tag="development-$(date -u +%Y%m%dT%H%M%SZ)-$(git rev-parse --short HEAD)"
active_task="$(aws ecs describe-services --cluster shiftbox-demo --services controller \
  --query 'services[0].taskDefinition' --output text)"
base_image="$(aws ecs describe-task-definition --task-definition "$active_task" \
  --query 'taskDefinition.containerDefinitions[?name==`controller`].image | [0]' --output text)"
test "$base_image" = "$repository@$(jq -er .controller_image_digest "$active_vars")"
printf '%s\n' "$base_image" > "$rollout/base-image"

cat > "$rollout/overlay/Dockerfile" <<'DOCKERFILE'
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
COPY shiftbox /usr/local/bin/shiftbox
DOCKERFILE

aws ecr get-login-password | docker --config "$rollout/docker" login --username AWS --password-stdin "$registry"
docker --config "$rollout/docker" build --pull --platform linux/amd64 \
  --build-arg BASE_IMAGE="$base_image" --tag "$repository:$tag" "$rollout/overlay"
docker --config "$rollout/docker" run --rm --platform linux/amd64 --entrypoint /bin/sh "$repository:$tag" \
  -ec '/usr/local/bin/shiftbox --help 2>&1 | grep "^usage: shiftbox setup "'
docker --config "$rollout/docker" push "$repository:$tag"
digest="$(aws ecr describe-images --repository-name "${repository#*/}" --image-ids imageTag="$tag" \
  --query 'imageDetails[0].imageDigest' --output text)"
docker --config "$rollout/docker" logout "$registry"
printf '%s\n' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$'
```

Use the overlay for controller and UI updates. If the change affects runtime packages, Dex, or the
container entry point, build the complete Linux amd64 image with `platform/container/Dockerfile`
and use the resulting ECR digest in the Terraform step.

### Plan and roll out through Terraform

The running database must have this build's schema version before switching the image. For a
build with new migrations, complete the application schema migration through the approved
upgrade procedure first.

Continue in the same shell. Review the saved plan before applying it. A controller-only release
updates the controller ECS service and replaces the controller and setup task definitions. Terraform
can show the setup operator policy as an update because it references the setup task family.
Investigate other changes, particularly to PostgreSQL, data volumes, networking, and permissions,
before applying.

```sh
jq --arg digest "$digest" '.controller_image_digest = $digest' \
  "$rollout/previous.tfvars.json" > "$rollout/next.tfvars.json"

terraform -chdir=platform/terraform/aws plan -input=false -state="$state" \
  -var-file="$demo/ecs-host.tfvars" -var-file="$rollout/next.tfvars.json" \
  -out="$rollout/demo.tfplan"

terraform -chdir=platform/terraform/aws show -json "$rollout/demo.tfplan" |
  jq -r '.resource_changes[] | select(.change.actions != ["no-op"] and .change.actions != ["read"])
    | [.address, (.change.actions | join(","))] | @tsv'
```

Once the plan matches the intended rollout, apply it and verify the running image.

```sh
terraform -chdir=platform/terraform/aws apply -input=false -state="$state" "$rollout/demo.tfplan"
aws ecs wait services-stable --cluster shiftbox-demo --services controller postgres
aws ecs describe-services --cluster shiftbox-demo --services controller postgres \
  --query 'services[].{service:serviceName,running:runningCount,desired:desiredCount,revision:taskDefinition,state:deployments[?status==`PRIMARY`].rolloutState|[0]}' \
  --output table

task="$(aws ecs list-tasks --cluster shiftbox-demo --service-name controller --desired-status RUNNING \
  --query 'taskArns[0]' --output text)"
aws ecs describe-tasks --cluster shiftbox-demo --tasks "$task" \
  --query 'tasks[0].{revision:taskDefinitionArn,status:lastStatus,digest:containers[?name==`controller`].imageDigest|[0]}' \
  --output json | tee "$rollout/running-task.json"
jq -e --arg digest "$digest" '.status == "RUNNING" and .digest == $digest' "$rollout/running-task.json"
cp "$rollout/next.tfvars.json" "$active_vars"
terraform -chdir=platform/terraform/aws output -state="$state" -raw management_origin
```

The demo has one controller task, so expect a brief interruption during replacement. If ECS still
shows `IN_PROGRESS` after the waiter exits, repeat the service status check until both services
report `COMPLETED`. Verify the changed feature while signed in at the printed management origin. For resource profiles, open
**Administration → Infrastructure → Resources**, choose `t3.medium` for a disposable profile,
confirm it displays 2 vCPUs and 4,096 MiB, and set the EBS disk to 32 GiB. Reload to check
it persisted, rename it, disable and re-enable it, then delete it and confirm it stays gone
after a reload. Leave existing profiles in place. Report the live review URL, ECS task revision,
and checks performed.

If the controller fails to stabilize or fails the smoke test, check the ECS service events and
the controller stream in CloudWatch log group `/shiftbox/shiftbox-demo/ecs`. To roll back a
controller-only release, generate a fresh plan against the same state using
`"$rollout/previous.tfvars.json"` in place of `"$rollout/next.tfvars.json"`. Review and apply it,
wait for both services, verify the previous digest in ECS, and restore that file to
`deployment-current.tfvars.json`.

## Testing

- Always write unit tests before writing code
- Test should reflect real world failure scenarios
- Each test must have a comment that describes the test so a reader understands what is being
  tested.
