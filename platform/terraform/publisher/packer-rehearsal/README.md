# Protected Ohio Packer rehearsal

The [manual workflow](../../../../.github/workflows/rehearse-packer.yml)
exercises the same pinned hosted runner, Packer recipe, and private EC2
Instance Connect tunnel as the release build. It creates an attempt-named
private AMI, verifies it, and removes the AMI and snapshot. It has no
write access to GitHub releases, packages, the publisher buckets, or
qualification. The release jobs continue to require their protected tags.

## GitHub environment proposal

Configure the following only after the workflow and environment have been
reviewed. Keep the opt-in off until the actual run is approved.

| Setting | Proposed value |
| --- | --- |
| Environment | `aws-packer-rehearsal` |
| Deployment rules | Only the branch `main`. Deny all tag refs. |
| Required reviewer | `jflatten` only, with administrator bypass disabled. Leave self-review available for the sole maintainer. |
| Enable variable | `SHIFTBOX_PACKER_REHEARSAL_ENABLED=false` until an approved run is queued. |
| Role variable | `SHIFTBOX_PACKER_REHEARSAL_ROLE_ARN` from this Terraform root. |
| Network variable | `SHIFTBOX_PACKER_REHEARSAL_NETWORK`, the reviewed Ohio `attempt` output projected to the hosted input schema, plus the pinned Canonical `source_ami`. |
| Network key | `SHIFTBOX_PACKER_REHEARSAL_SSH_KEY`, one-attempt private key matching the Terraform public key. |
| Verification key | `SHIFTBOX_PACKER_REHEARSAL_GUEST_PUBLIC_KEY`, the 32-byte guest-update public key in one-line base64. |

The workflow requires the immutable repository and owner, the sole dispatch
actor, the exact reviewed main SHA with passing Checks, a unique
`rehearsal-<1–8 lowercase alphanumeric characters>` attempt, and a first run
rather than a rerun. Jobs are serialized. The Ohio network schema currently
uses `v1.0.0-rc.1` as an input label to exercise the release recipe. The
created AWS artifact uses `packer-rehearsal` provenance and a distinct
name. It never enters the candidate handoff.
[Protected environments](https://docs.github.com/en/actions/how-tos/deploy/configure-and-manage-deployments/manage-environments)

## Plan the role

This root uses a private, separate state scoped to the verified publisher
account in Ohio and reuses its GitHub OIDC provider. The default plan
creates a closed `shiftbox-publisher-packer-rehearsal` role with a deny
trust statement and no inline permissions. It makes no changes to the
provider or release roles. A later bound plan uses the exact network outputs,
Canonical AMI and snapshot, reviewed main SHA, initial dispatch run ID, and
attempt deadline. Generate the bound plan after the network exists and the
operator can verify the pending run. Review it before authorizing any apply.

The bound trust accepts the GitHub audience and immutable
`repo:jflatten@11561330/ShiftBox@1457179716:environment:aws-packer-rehearsal`
subject, `refs/heads/main`, the immutable maintainer ID, and the named
rehearsal workflow. It also restricts the STS session name and expiration.
The caller supplies the session name. GitHub's protected environment
requires explicit approval. AWS independently enforces the
endpoint, subnet, source-image pins, attempt tags, and expiry.
[GitHub OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) · [AWS STS condition keys](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_iam-condition-keys.html)

The role can read Ohio build inventory, open the exact private TCP 22 tunnel,
launch the reviewed `t3.medium` with a tagged 32 GiB gp3 root, create the
private image, set its description and IMDSv2, and stop or remove the
attempt's own resources. The pinned Packer Amazon plugin uses
`ec2:DescribeRegions` before launching. That request must use the Ohio
endpoint and occur before expiry, like the other inventory reads.
A launch or image-creation request is authorized before EC2 assigns IDs
to the new resources. The policy uses their resource types for creation
and keeps the account or region scope, exact launch inputs, and required
attempt tags. Operations on existing resources keep the ID prefixes and
ownership checks.
A full binding allows no AMI copy, AMI launch
permission change, snapshot sharing, bucket access, IAM delegation, or
network creation. Inventory calls that require a wildcard stay bounded by
region and time. After the deadline, the identity policy no longer allows
the build actions even if STS credentials have time left.
[EC2 action and condition reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html) · [Tunnel permissions](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/permissions-for-ec2-instance-connect-endpoint.html)

## Teardown and review

The build command is limited to 40 minutes inside a 50-minute job. Its
final step checks account, attempt ownership, private sharing attributes,
and the AMI-to-snapshot relationship before deleting artifacts. It covers
success and failures that leave the runner available. A staffed operator
verifies the inventory independently, removes any residual private AMI
and snapshot after runner loss, reconciles and removes the Ohio network,
and clears the temporary GitHub key and variables. The network's existing
expiry schedules provide independent cleanup of compute and egress. The
operator closes the rehearsal role after the attempt. Keep the separately
reviewed two-hour lease and approved spend reserve.
[Ohio network plan](../ohio-build/README.md) · [Hosted Packer plan](../../../packer/aws/publisher-runner-plan.md)

The Terraform mock tests cover both the closed role and a fixture-bound
role. Workflow, hosted handoff, private cleanup, and Packer recipe tests
run without provisioning. The fixture's endpoint and resource IDs exist
only for offline review.
