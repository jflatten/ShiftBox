# Iterate on the AWS deployment before releasing

Use `shiftbox-testing` (`888293065132`) in Ohio. Fresh installations use the
same checked-in CloudFormation YAML and pinned inputs as release qualification.

## Deploy the tip of main from GitHub Actions

Run **Deploy private AWS test from main** in Actions with **main** selected.
The workflow checks out the dispatched SHA and stops if main moves before
deployment. It builds the controller, browser UI, macOS helper, and an
encrypted workspace AMI from that commit, then deploys `sbtest-main` in Ohio.
The workflow uses the checked-in YAML and passes the catalog and its checksum
through **Release inputs**. Passing Checks and a signed tag are part of the
release workflow and are independent of this test.

### Set up the private test destination

1. Create the `aws-main-test` GitHub environment and allow deployments from
   protected `main` only. Use the existing GitHub OIDC provider in
   `shiftbox-testing`. Review and apply the [main test Terraform configuration](../terraform/main-test/main.tf).
   Supply the private Packer network and the reviewed
   `shiftbox-main-test-cloudformation` IAM policy for the single `sbtest-main`
   stack. That policy must allow creation and cleanup of the template resources, including
   passing only the stack's service roles. The workflow identities cannot
   modify this policy or operate publisher resources.
2. Create or select an Ohio EC2 Instance Connect Endpoint in the private build
   subnet. Tag it `shiftbox-project=shiftbox` and
   `shiftbox-cluster-name=sbtest-main`. Configure IPv4 with client IP
   preservation off, allow SSH from the endpoint security group to the
   builder group, and provide outbound access for the guest build.
3. Set up the **private** GHCR package `jflatten/shiftbox-main-test` and grant
   this repository Actions write access. In Ohio Secrets Manager, create
   `shiftbox-main-test-ghcr` using the default Secrets Manager key. Store a
   GHCR username and read-only package token in its `username` and
   `password` fields. The stack's ECS execution role reads it when pulling
   the private image. [GHCR package access](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility) · [ECS private registry credentials](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/private-auth.html)
4. Set these variables in the GitHub environment.

   | Variable | Value |
   | --- | --- |
   | `SHIFTBOX_MAIN_TEST_BUCKET` | The Terraform `staging_bucket` output. |
   | `SHIFTBOX_MAIN_TEST_BUILD_NETWORK` | The Terraform `workflow_build_network` JSON output. |
   | `SHIFTBOX_MAIN_TEST_GUEST_UPDATE_PUBLIC_KEY` | Single-line base64 of the 32-byte Ed25519 guest-update public key. |
   | `SHIFTBOX_MAIN_TEST_HOST_AMI` | Verified Ohio Amazon ECS host AMI ID. |
   | `SHIFTBOX_MAIN_TEST_POSTGRES_IMAGE` | The release lane's digest-pinned PostgreSQL 17 Debian image. |
   | `SHIFTBOX_MAIN_TEST_STACK_PARAMETERS` | JSON object with `ServiceSubnet`, `WorkspaceSubnet`, `BrowserAccess`, and the standard Route 53, Let's Encrypt, and ingress inputs for the tested form. |

The controller and AMI stay private. The workflow passes the helper and
template through a private, versioned S3 bucket with an expiration rule.

### Verify the deployment

1. Open the stack from the Action summary and use **ManagementOrigin** and
   **SetupClaimSecretArn** from its Outputs tab to complete first login.
2. After activation, sign in again and verify that the default AMI and
   **Small**, **Medium**, and **Large** profiles appear. Start a workspace
   and open its IDE to verify guest enrollment.
3. Record the tested SHA from the summary. If `main` advances before the
   release, repeat the test on the new SHA.
4. Delete the test workspaces and the `sbtest-main` stack after verification.
   Use the stack's resource inventory to remove its retained installation
   volume and generated secrets. Deregister the run's private workspace AMI,
   delete its snapshot, and remove the corresponding private GHCR version.

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
