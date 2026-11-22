# Build the AWS workspace AMI

The AWS recipe defaults to a private, encrypted EBS-backed Ubuntu 24.04 LTS
Minimal amd64 image in the selected account and region. It includes the
ShiftBox guest agent, pinned development tools, and the offline repair
environment. A separate mode prepares the clean AMI for public release.
The existing UTM recipe has a separate build workflow.

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

Set `GUEST_AGENT_RELEASE` when a new AMI artifact ID must stay within the controller's existing guest release compatibility window. The build uses that value in both the guest binary and the manifest. By default it uses the artifact's release ID.

Packer builds a private AMI, locks the temporary Ubuntu login, disables
SSH on subsequent boots, and clears build-time machine identity and
cloud-init history before capture. The output path printed after the
build contains the Packer manifest. Its `artifact_id` identifies the
region and the published AMI. Its `custom_data` records the release,
source AMI publisher, source revision, guest binary and tooling digests,
and minimum root disk size. Platform admission reads the published
AMI's ownership and root snapshot metadata from EC2 and validates guest enrollment
and cleanup before making the release available for workspace launch.

## Public release prerequisites

The public release uses a dedicated, maintainer-approved AWS publishing context.
Record its approved account ID as the expected publisher in release configuration.
The private demo is the shared testbed. Qualify public artifacts separately from
consumer accounts that have no private access grants to them.

| Check | Evidence required before public release |
| --- | --- |
| Publisher identity | Confirm the approved account through STS, then verify the account owns the public AMIs and dedicated release buckets. |
| AMI publication | Verify effective AMI Block Public Access and EBS encryption-by-default policy in `us-east-1`, `us-east-2`, `us-west-1` and `us-west-2`. The public artifact uses a clean, unencrypted backing snapshot in each region. Customer instances still launch with encrypted storage. Record exact regional AMI and snapshot IDs, verified ownership, and retention or deprecation status. |
| Packer runner | Verify the runner can reach the dedicated build subnet over its approved SSH path, the source image pin is appropriate for a clean public build, and the build role can clean up temporary resources. |
| S3 release assets | Confirm the four dedicated regional buckets have versioning, SSE-S3, bucket-owner enforcement, ACL blocks and scoped TLS-only public reads of released object versions. Check effective organization, account and bucket Block Public Access policy. Verify the release versions are protected from deletion and anonymous list, write and unrelated reads are denied. |
| GitHub OIDC | Verify the publication and independent read-only verification roles, including audience, repository and protected release-context trust. Confirm expected permissions before running a protected version-tag rehearsal. |
| Qualification | Identify independently authorized cross-account consumers for all four region checks and a disposable Ohio account for the full install. Record resource cleanup ownership and keep account access details and raw evidence private. |

The publisher approval, resource identifiers, role configuration, runner and
consumer-account evidence are pending. Keep demo settings and data outside the
public publication review.

References: [public regional AMIs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/sharingamis-intro.html),
[EBS encryption defaults](https://docs.aws.amazon.com/ebs/latest/userguide/encryption-by-default.html),
[S3 Block Public Access](https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html),
[GitHub OIDC trust in IAM](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-idp_oidc.html),
and the [public release qualification procedure](../../../specs/010-public-aws-distribution/quickstart.md).

### Build the public candidate image

After approval, use a clean checkout of the RC commit and a publisher-only
`.pkrvars.json` file with the deployment keys above and the pinned regional
Canonical source AMI. Use the approved publisher AWS session and the same
release tag that stamped the controller image.

```sh
PUBLIC_AMI_BUILD=1 RELEASE_CANDIDATE="$candidate_tag" \
  RELEASE_PUBLISHER_ACCOUNT_ID="$approved_publisher_account" \
  platform/packer/aws/build-image.sh "$candidate_tag" /path/to/guest-update.pub \
    /path/to/publisher.local.pkrvars.json
```

The build validates the caller account, EBS default encryption, Canonical image
identity, and sealed guest state. It leaves the output AMI private and reports
its local manifest path after verifying the owner, root snapshot and build
provenance. The manifest includes `sourceRegion` and `publisherAccountId` for
the separate copy, qualification and publication steps. Customer launch volumes
use encryption independently.

Run `platform/packer/aws/public-recipe.test.sh` for the local preflight,
provenance and clean-root fixtures. The test uses only temporary files and AWS
command stubs.
