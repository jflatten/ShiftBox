# Ohio hosted Packer build plan

The protected candidate build runs on a fresh GitHub-hosted Ubuntu VM. Packer
launches the only EC2 instance used for the build, a private `t3.medium` in the
publisher's Ohio account. The operator reviews a separate Terraform plan for
each attempt. Regional copy and sharing retain their existing protected jobs
and exact artifact handoffs.

## Established CI pattern

HashiCorp's GitHub Actions tutorial checks out the release source, installs
Packer, initializes the builder and runs the image build. The workflow follows
HashiCorp's init, validate, build sequence using Packer 1.14.2, Amazon plugin
1.8.4, the published binary checksum, and the repository's Go toolchain.
AWS's GitHub Actions guidance uses short-lived OIDC credentials. ShiftBox keeps
the existing immutable candidate environment subject and tag protections.
The existing manifest remains the build output.
[HashiCorp Actions tutorial](https://developer.hashicorp.com/packer/tutorials/cloud-production/github-actions) · [HashiCorp CI validation example](https://github.com/hashicorp/web-unified-docs/blob/main/content/well-architected-framework/docs/docs/define-and-automate-processes/process-automation/fully-automated.mdx) · [AWS OIDC guidance](https://aws.amazon.com/blogs/security/use-iam-roles-to-connect-github-actions-to-actions-in-aws/)

GitHub-hosted runners use a fresh VM for each standard job. This repository is
personally owned and public, and its repository-level runner labels do not
restrict which eligible workflow can take a runner. The release job needs
no runner registration or separately hosted worker.
[GitHub-hosted runners](https://docs.github.com/en/actions/concepts/runners/github-hosted-runners) · [GitHub runner group controls](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)

Packer's normal private-IP SSH communicator supports a SOCKS proxy. The
ShiftBox loopback bridge forwards that stream through AWS CLI
`ec2-instance-connect open-tunnel`. The Amazon plugin's native Session Manager
option requires an SSM-enabled instance profile and the workstation plugin.
EC2 Instance Connect works with the existing imported SSH key and keeps the
builder without an instance profile.
[Packer SSH communicator](https://developer.hashicorp.com/packer/docs/communicators/ssh) · [Amazon EBS builder](https://developer.hashicorp.com/packer/integrations/hashicorp/amazon/latest/components/builder/ebs) · [AWS CLI OpenTunnel](https://docs.aws.amazon.com/cli/latest/reference/ec2-instance-connect/open-tunnel.html)

## Terraform and IAM review

Use a separate Ohio Terraform root and private state for the attempt. The
publisher account and region fences must pass before planning. Review the
following network scope and its exact delta. The reusable configuration is in
the [Ohio build Terraform root](../../terraform/publisher/ohio-build/README.md).
The read-only review plan showed 32 AWS creates and one local deadline guard,
with zero updates or destroys. The AWS inventory includes the dedicated
network, endpoint, public key and cleanup controls. The reusable configuration
keeps account and network inputs in operator-provided files. Generate a fresh
private saved plan with current attempt inputs before any separately approved
provisioning.

| Planned resource | Scope |
| --- | --- |
| Dedicated VPC and subnets | One VPC, one private build subnet and one egress subnet in a verified Ohio availability zone. Neither subnet auto-assigns public addresses. |
| Egress | An internet gateway, temporary NAT gateway and EIP. The private subnet routes outbound through NAT for Ubuntu packages, Docker, Node.js, and registry sources. Default builder egress is TCP 443. Confirm whether the selected base uses signed Ubuntu APT sources on TCP 80 before selecting the optional builder-only rule. |
| EC2 Instance Connect Endpoint | An IPv4 endpoint in the build subnet with client IP preservation disabled. Endpoint egress is TCP 22 to the exact builder security group, and the builder accepts TCP 22 only from that endpoint group. |
| Imported key pair | One attempt's Ed25519 SSH public key. Keep the private key outside Terraform and EC2 user data. |
| Expiry | One attempt-scoped Lambda and five bounded one-time Scheduler invocations. The cleanup is independent of GitHub and expires with the attempt. |

Verify the first-use EC2 Instance Connect service-linked role separately. AWS
may create it at endpoint creation. Keep that account-level role outside the
attempt's state and teardown. The AMI build policy needs
`ec2-instance-connect:OpenTunnel` only for the exact endpoint ARN, private
builder CIDR, TCP 22, current attempt tags, and a maximum 2,700-second tunnel.
The limit keeps one Packer provisioning session comfortably within the
50-minute job and one-hour role session. The AWS CLI also needs
`ec2:DescribeInstanceConnectEndpoints` in Ohio to resolve the endpoint. No
instance profile, public SSH ingress, `iam:PassRole`, or endpoint-management
grant is needed in the build role. Copy and share policies receive no tunnel
permission.
[AWS endpoint permissions](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/permissions-for-ec2-instance-connect-endpoint.html) · [Endpoint security groups](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/eice-security-groups.html)

## Protected job inputs

After a separately approved network deployment, prepare the
`aws-release-candidate` environment inputs from the exact attempt outputs and
reviewed Canonical AMI. The variable `SHIFTBOX_RELEASE_BUILD_NETWORK` takes
the following JSON. The fields other than `source_ami` come from the attempt
output.

```sh
terraform output -json attempt | jq -c --arg source_ami "$reviewed_base_ami" '
  {account,attempt_id,builder_group_id,candidate_tag,deadline,endpoint_id,packer_key_pair,
   packer_public_key_sha256,private_subnet_cidr,private_subnet_id,region,source_ami:$source_ami,vpc_id}'
```

Store the matching one-attempt SSH private key as
`SHIFTBOX_RELEASE_BUILD_SSH_KEY` in that protected environment only. Set
`SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY` to the one-line base64 encoding of
the existing 32-byte guest-update public key. The build step verifies the SSH
key pin, writes the key into an owner-only temporary directory, and clears
the key and bridge on exit. Delete the environment's temporary SSH secret and
EC2 key pair when the attempt ends, including after cancellation.

The hosted build validates the exact publisher, release, region and
attempt before invoking Packer. The loopback bridge rechecks AWS identity
and endpoint ownership and placement, then validates the builder's private
address, subnet, security group, attempt tags and identity before opening
TCP 22. Reconnects must target the same builder. Local build logs and keys
stay outside the sanitized Packer artifact. Leave enough time on the AWS
attempt deadline for the full 50-minute job before approving the build.

## Teardown and rehearsal

Verify the expiry schedules after any future apply and before approving the
build job. A partial apply before schedule creation needs staffed teardown.
The cleanup checks attempt ownership and deletes eligible disposable compute
and detached storage, drops NAT, and releases the EIP only when dissociated.
It has no AMI or snapshot deletion permission. The operator reconciles private
state and verifies complete removal of the attempt resources and GitHub
secret after success, failure or cancellation.

Before using the protected tag, separately approve a disposable,
nonpublishing rehearsal for the exact hosted runner image, CLI, key and
Packer inputs. Verify private SSH, blocked alternate endpoints/ports and
cross-attempt targets, guest package egress, session and job cancellation,
guest tagging, and full teardown after runner loss. Test effective IAM against
actual resource IDs. Inventory any existing private candidate image instead
of rebuilding over it.

The earlier two-hour estimate for 10 GB of NAT processing and 1 GB of
outbound transfer was $0.92 with a separate EC2 runner. Removing that
instance and its root reduces the same modeled attempt to about $0.74.
The estimate excludes retries, AMI copies, and snapshot retention. Confirm
current rates and aggregate headroom under the $30 monthly cap before
authorizing a paid rehearsal.
[AWS T3 pricing](https://aws.amazon.com/ec2/instance-types/t3/) · [AWS NAT pricing](https://aws.amazon.com/vpc/pricing/) · [AWS EBS pricing](https://aws.amazon.com/ebs/pricing/) · [GitHub Actions billing](https://docs.github.com/en/actions/concepts/billing-and-usage)
