# Build the AWS workspace AMI

The AWS recipe builds one private, encrypted EBS-backed Ubuntu 24.04 LTS
Minimal amd64 image in the selected account and region. It includes the
ShiftBox guest agent, pinned development tools, and the offline repair
environment. The existing UTM recipe has a separate build workflow.

Use Packer 1.14.2, the repository's Go toolchain and `shasum`. Use AWS credentials
for a release builder authorized to create and clean up temporary build
resources and publish an AMI. Run Packer on a runner with SSH access to
the private build subnet. The supplied build security group should
permit SSH only from that runner and allow the installer to reach its
package and image sources. The workspace security group serves
workspaces and does not need a build SSH rule.

Resolve the Canonical Ubuntu Minimal image ID for the target region
and record the exact ID in the release variables. The Public Parameter
Store path is
`/aws/service/canonical/ubuntu/server-minimal/noble/stable/current/amd64/hvm/ebs-gp3/ami-id`.
The recipe checks the pinned ID against Canonical's publisher account
and amd64, Noble Minimal, HVM and EBS metadata.

Put the following deployment values in a local `*.local.pkrvars.hcl` file.

```hcl
region                  = "us-east-1"
source_ami              = "ami-0123456789abcdef0"
build_subnet_id         = "subnet-0123456789abcdef0"
build_security_group_id = "sg-0123456789abcdef0"
cluster_name            = "shiftbox-demo"
environment             = "demo"
build_key_pair          = "shiftbox-demo-packer"
build_private_key_file  = "/path/to/private/build-key"
```

The key pair must already exist in the build region and match the local private key. A runner outside the subnet can use a private SOCKS tunnel by setting `build_ssh_proxy_host` and `build_ssh_proxy_port`.

From the repository root, initialize the pinned plugin and check the
recipe without provisioning.

```sh
packer init platform/packer/aws
platform/packer/aws/test-recipe.sh
```

Build with the agent release ID and the publisher's 32-byte raw
Ed25519 verification key.

```sh
platform/packer/aws/build-image.sh v0.1.0 /path/to/guest-update.pub /path/to/deployment.local.pkrvars.hcl
```

Packer builds a private AMI, locks the temporary Ubuntu login, disables
SSH on subsequent boots, and clears build-time machine identity and
cloud-init history before capture. The output path printed after the
build contains the Packer manifest. Its `artifact_id` identifies the
region and the published AMI. Its `custom_data` records the release,
source AMI publisher, source revision, guest binary and tooling digests,
and minimum root disk size. Platform admission reads the published
AMI's ownership and root snapshot metadata from EC2 and validates guest enrollment
and cleanup before making the release available for workspace launch.
