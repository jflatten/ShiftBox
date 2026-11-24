# Iterate on the AWS deployment before releasing

Use `shiftbox-testing` (`888293065132`) in Ohio. Render the current checkout
against known public pins, then replace either artifact independently with an
exact private build. The public release workflow and qualification flags are
outside this development path.

## Install once and finish in the private UI

1. Choose the existing service and workspace subnets in Ohio testing account
   `888293065132`. Default VPC subnets are supported. ShiftBox disables
   public IPv4 assignment for its host, ECS tasks and workspaces. Provide
   outbound access to AWS and the image registries, plus private browser routing
   and DNS forwarding to the VPC. The stack creates workload security groups
   and installation DNS records. It creates no VPC, subnet, route table,
   NAT gateway, internet gateway or Elastic IP.

   Check reachability for the configured ECS, EC2, STS, Secrets Manager and
   CloudWatch Logs endpoints, the private ECR image and the pinned PostgreSQL
   registry image. An IPv6 egress-only route covers destinations reachable
   over IPv6. Verify the endpoint addresses and workload connectivity for
   the path you supply. [AWS ECS dual-stack networking](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-networking-awsvpc.html)
2. With the verified RC3 assets from [Render a baseline](#render-a-baseline),
   build and render the private development controller. The RC3 workspace AMI
   stays pinned unless you select an explicit private AMI.

   ```sh
   controller=$(platform/development/build-controller.sh v1.0.0-rc.3)
   platform/development/render-aws.sh dist/rc3-baseline-assets dist/aws-one-pass \
     --one-pass --controller "$controller"
   ```

3. Upload `dist/aws-one-pass/shiftbox-aws-development.json` in the
   [Ohio CloudFormation console](https://us-east-2.console.aws.amazon.com/cloudformation/home?region=us-east-2#/).
   Choose a unique stack name and DNS suffix, the existing VPC and subnets,
   and the service subnet's Availability Zone. For **Inbound security group
   rule for UI access**, enter the IPv4 range AWS sees for users connecting
   over HTTPS on port 8443. Review the change set and acknowledge named IAM
   capabilities.
   Set **OCI image cache size (GiB)** in the Installation section for the
   separate encrypted cache disk. The default is 20 GiB for up to five active
   users. Start with 50 GiB for 6 to 20 users or 100 GiB for 21 to 50.
   ShiftBox tags the stack resources and workspaces with `shiftbox-` keys,
   including `shiftbox-project` and `shiftbox-cluster-name`.
   For guided CLI review, run
   `platform/development/install-aws.sh dist/aws-one-pass .shiftbox/dev-one-pass`.
   Resume with the same arguments and `--resume`.
4. Open **Outputs** after the stack completes. Retrieve the public PEM from
   `BrowserCaSecretArn` in the AWS Secrets Manager console and trust it
   once in your browser or OS. Retrieve the separate claim from
   `SetupClaimSecretArn` with your authorized AWS identity. Keep the claim
   out of URLs, terminal history and shared evidence.
5. Open `ManagementOrigin`. Enter the claim and administrator email, then
   enter the new password twice. Sign in with that email and password,
   validate the deployed database and browser certificate,
   save workspace capacity, and activate. When ShiftBox reports it is online,
   sign in again at the same URL. On an empty development installation,
   ShiftBox checks the exact regional release image in EC2 and adds enabled
   **Small**, **Medium** and **Large** profiles. All three pin that image.
   Choose one when creating a project template. Review and edit the profiles in
   **Administration → Infrastructure → Resources**.

   Small uses a burstable `t3.medium` with 2 vCPUs, 4 GiB RAM and 32 GiB EBS
   for editing and light builds. Medium uses `m6a.large` with 2 vCPUs,
   8 GiB RAM and 64 GiB EBS for an IDE and a typical devcontainer.
   Large uses `m6a.xlarge` with 4 vCPUs, 16 GiB RAM and 96 GiB EBS for
   build concurrency and supporting services. ShiftBox uses a compatible
   fallback if the preferred type is unavailable in the selected workspace
   Availability Zone. [EC2 general purpose instance specifications](https://docs.aws.amazon.com/ec2/latest/instancetypes/gp.html)

Browser TLS and PostgreSQL TLS use separate installation issuers. The host
generates and retains the leaf identities on encrypted storage. The database
private key is mounted only into PostgreSQL. An interrupted owner claim can
resume with the same email and password. After successful
creation the claim is spent, and activation retires the setup API.

## Connect GitHub repositories

After activating, sign in as the administrator and open **Administration →
Settings → GitHub App**. Enter the GitHub organization that owns the
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

## Render a baseline

Use AWS CLI v2, GitHub CLI, jq, and Python with PyYAML. The public download is
the baseline for unchanged artifact pins and the catalog. The renderer
checks the release asset digests before reading the published catalog.

```sh
gh release download v1.0.0-rc.3 --repo jflatten/ShiftBox \
  --pattern distribution-manifest.json --pattern install.sh \
  --pattern shiftbox-aws-cloudformation.yaml --pattern shiftbox-aws-support.tar.gz \
  --dir dist/rc3-baseline-assets

platform/development/render-aws.sh dist/rc3-baseline-assets dist/aws-development-1
```

The local `shiftbox-aws-development.json` restricts deployment to the testing
account in Ohio. Upload it using **CloudFormation → Create stack → With new
resources (standard) → Upload a template file**. Use stack and deployment
names unique to the development attempt and choose existing private service
and workspace subnets in one VPC. Use the service subnet's Availability Zone.
Provide private routing, egress and DNS forwarding independently. Review the
CloudFormation change set. The current template still follows the [staged
setup guide](../cloudformation/aws/README.md). [AWS console upload](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/cfn-console-create-stack.html)

## Iterate on one artifact at a time

**Controller.** Use Docker with Buildx and the private Ohio ECR repository
`shiftbox-development`, managed in [development-registry](../terraform/development-registry/main.tf).
The builder fences the account with STS, uses a temporary Docker credential
directory, pushes an immutable `dev-*` tag, and returns the exact digest URI.
Run the builder on macOS so it can sign and package the helper. Pass the
stack's management HTTPS origin as the second argument to prefill it in the
helper. Without that argument, the image includes the portable helper, which
prompts for the URL on first launch.
Use `v1.0.0-rc.3` as the guest compatibility version while exercising RC3
guests. The pinned private image URI identifies the development build.

```sh
controller=$(platform/development/build-controller.sh v1.0.0-rc.3)
platform/development/render-aws.sh dist/rc3-baseline-assets dist/aws-development-2 \
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
platform/development/build-workspace.sh v1.0.0-rc.3 \
  /path/to/guest-update.pub /path/to/testing.local.pkrvars.json

platform/development/render-aws.sh dist/rc3-baseline-assets dist/aws-development-3 \
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
