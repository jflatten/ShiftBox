# Iterate on the AWS deployment before releasing

Use `shiftbox-testing` (`888293065132`) in Ohio. Fresh installations use the
same checked-in CloudFormation YAML and pinned inputs as release qualification.

## Install once and finish in the browser

1. Confirm the testing account and choose existing service and workspace
   subnets in the same VPC. Supply outbound access for AWS and container
   registries. Arrange your browser route and DNS forwarding.
2. Open the release’s Ohio CloudFormation link. For a manual upload, verify
   the release artifacts and extract only the regional **Release inputs**.

   ```sh
   platform/development/render-aws.sh dist/verified-release-assets dist/ohio-inputs
   ```

   Use `shiftbox-aws-cloudformation.yaml` from that same release and the
   contents of `dist/ohio-inputs/release-pins.json`. The helper writes the
   input values. The YAML comes from the candidate commit.
3. Choose a unique stack name, the two subnets and **Private** or **Public**
   browser access. Select the public Route 53 zone and supply the Let’s
   Encrypt contact, agreement and approved source IPv4 range. Review the
   named IAM requirements and execute one CREATE change set.
4. Wait for **CREATE_COMPLETE**. Retrieve the claim referenced by
   `SetupClaimSecretArn` and visit `ManagementOrigin`. Create the first
   administrator, sign in, validate the database and certificate, save
   workspace capacity and activate. Sign in again at the same URL.

Use the release catalog to validate and pin the regional workspace image
if the installation needs a resource profile. [Installation guide](https://www.shiftbox.dev/install/)

## Connect GitHub repositories

After activating, sign in as the administrator and find **GitHub Integration**
under **Administration → Integrations**. Enter the GitHub organization that owns the
repositories or leave it blank for a personal account. Select **Set up GitHub
App**, confirm the prefilled registration on GitHub, and choose the repositories
when installing. Keep the private ShiftBox URL reachable from your browser for
both returns. Use **Check installation** if your organization approves
installation later. ShiftBox stores the app credentials encrypted with the
installation and enables repository authorization when GitHub verifies the
installation.

Each workspace owner uses **Account → Connect GitHub** and gives GitHub their
own consent. An administrator who wants to launch a workspace authorizes their
own account in the same way.

When cleaning up a disposable attempt, include the retained setup claim and
browser CA references alongside the volume and database secrets. Remove the
attempt's browser CA from the local trust store. The CLI enables termination
protection, so disable it on the verified disposable stack before deleting it.

## Use a retained legacy baseline

The development renderer supports previously downloaded schema 1 assets.
It checks the release digests before reading the template or catalog. Keep
the legacy asset directory with the appropriate installation instructions.

## Iterate on one artifact at a time

For a controller-only update, use the approved existing testing stack and
review its CloudFormation change set. The private template overrides below
are for retained schema 1 development baselines.

**Controller.** Use Docker with Buildx and the private Ohio ECR repository
`shiftbox-development`, managed in [development-registry](../terraform/development-registry/main.tf).
The builder fences the account with STS, uses a temporary Docker credential
directory, pushes an immutable `dev-*` tag, and returns the exact digest URI.
Run the builder on macOS so it can sign and package the helper. Pass the
stack's management HTTPS origin as the second argument to prefill it in the
helper. Without that argument, the image includes the portable helper, which
prompts for the URL on first launch.
Use the running stack’s guest compatibility version. The pinned private image URI identifies the development build.

```sh
controller=$(platform/development/build-controller.sh "$guest_compatibility" "$management_origin")
platform/development/render-aws.sh dist/legacy-baseline-assets dist/aws-development-2 \
  --controller "$controller"
```

The development template gives the controller task execution role and its
permissions boundary read-only pulls from that exact repository. The browser
and controller come from the same image build. [ECS task execution permissions](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_execution_IAM_role.html#task-execution-ecr-permissions)

**Workspace AMI.** Use the pinned Packer plugin and the [existing private
workspace recipe](../packer/aws/README.md). Prepare the local `.pkrvars.json`
with an Ohio Canonical Ubuntu source AMI, existing private build subnet,
same-VPC build security group, private SSH route and build key, and
`cluster_name=shiftbox-development`. Keep keys and variables outside Git.
The wrapper binds Packer to the testing profile and uses an encrypted private
build. It prints the local manifest path.

An example `testing.local.pkrvars.json` is shown below. Substitute your existing
testing network, key pair and verified Canonical Ohio image.

```json
{
  "region": "us-east-2",
  "source_ami": "ami-0123456789abcdef0",
  "build_subnet_id": "subnet-0123456789abcdef0",
  "build_security_group_id": "sg-0123456789abcdef0",
  "cluster_name": "shiftbox-development",
  "environment": "development",
  "build_key_pair": "shiftbox-development-packer",
  "build_private_key_file": "/absolute/path/to/private-build-key"
}
```

```sh
packer init platform/packer/aws
platform/development/build-workspace.sh "$guest_compatibility" \
  /path/to/guest-update.pub /path/to/testing.local.pkrvars.json

platform/development/render-aws.sh dist/legacy-baseline-assets dist/aws-development-3 \
  --workspace /path/to/packer/manifest.json
```

The renderer checks the AMI owner, privacy, encryption and Packer tags with
EC2 and permits only that additional AMI. In **Administration → Infrastructure
→ Platform**, import the private Packer manifest, observe the canary and pin
it on the dev resource profile. Use both flags when the change spans controller
and guest. Compare the running ECS image digest and actual workspace instance
AMI with the selected inputs. Keep the untouched baseline available to isolate
whether a failure is in the template, controller, or guest.

## End a development window

Check budget headroom before building or deploying. Close the window by
deleting its workspaces, deleting its exact development stack, and verifying
its retained EBS and secret dispositions. Preserve the selected VPC, subnets,
private router and permanent credentials. After checking that no other live
dev stack references them, delete the exact unused ECR digest and deregister
the private test AMI and its exclusive snapshot. Never delete the public pins.
Record changes against the development build and verify private browser access,
workspace enrollment and teardown before introducing a new RC tag.

Formal qualification later verifies the exact public candidate separately.
