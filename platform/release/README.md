# Public AWS release operations

## Repository readiness

Repository verification on 2026-11-21 confirmed these settings and outstanding
prerequisites for the first official release.

| Area | Observed status | Before an official tag |
| --- | --- | --- |
| Source and CI | The local `origin/main` snapshot, `7e76531`, contains project documentation and lacks the Go module, UI, Makefile and release workflows. The public repository uses `main` as its default branch. The feature checkout at `48f4d92` passed its Go and UI checks on GitHub. [Repository](https://github.com/jflatten/ShiftBox) · [Checks #35](https://github.com/jflatten/ShiftBox/actions/runs/44947949073) | Integrate reviewed buildable source and release workflows into protected `main` through a maintainer-approved change. Verify the required checks on the exact candidate commit. |
| Tag rules | GitHub readback confirmed two active tag rulesets for `refs/tags/v*`. The creation rule has one `User` bypass, repository owner and sole release approver `jflatten`. The separate update and deletion rule has no bypass. The existing CodeQL branch rule remains active. [Creation ruleset](https://github.com/jflatten/ShiftBox/rules/27815519) · [Update and deletion ruleset](https://github.com/jflatten/ShiftBox/rules/27815516) | Configure the workflow maintainer allowlist for the sole approver. Exercise allowed and denied operations in the approved release rehearsal. |
| Release immutability | Enabled and verified through authenticated GitHub API readback. The public release page has no release assets to inspect. [Repository settings](https://github.com/jflatten/ShiftBox/settings) · [Public releases](https://github.com/jflatten/ShiftBox/releases) | Verify during rehearsal that all assets are attached before publication and the published release is immutable. |
| GHCR ownership and visibility | Both the planned public package page and the authenticated user package API returned HTTP 404. A linked, anonymously pullable public package remains unverified. [Package page](https://github.com/jflatten/ShiftBox/pkgs/container/shiftbox) · [Container registry](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry) | Verify the publisher namespace, package-to-repository linkage and publishing workflow access with the package owner. Confirm anonymous pull of the exact published `linux/amd64` digest from a clean client after authorized publication. |

These observations are development evidence. The first candidate must satisfy
the [release publication contract](../../specs/010-public-aws-distribution/contracts/release-publication.md)
and the [exact-artifact qualification procedure](../../specs/010-public-aws-distribution/quickstart.md).

## Cross-account public AMI probe

For a bounded test-account run and independently scheduled cleanup, use the
[on-demand qualification workflow](../qualification/README.md). Review its
aggregate AWS budget and exact candidate gates before any paid dispatch.

Use the isolated Terraform fixture in
[public-ami-launch](../terraform/publisher/tests/public-ami-launch) with a separate
approved customer account and its normal AWS credentials. Confirm the customer
identity differs from the publisher and the AMI requires only the public launch
permission. Use a fresh state directory for each of the four manifest entries.
Never reuse the demo or publisher state for these probes.

In each working copy of the fixture, set `customer_account_id`,
`publisher_account_id`, `aws_region`, `availability_zone`, `ami_id` and the unique
test `name` in a private local variables file. Use that region's exact manifest
AMI. Then initialize the pinned provider and review a saved plan.

```sh
terraform init -lockfile=readonly
terraform plan -var-file=probe.tfvars -out=probe.tfplan
terraform show probe.tfplan
```

During authorized qualification, apply the reviewed probe plan. Record the
instance and encrypted root IDs from Terraform outputs. Verify the EC2 status
checks and that the instance uses the expected public AMI, has no public IP and
has no deployment role. The fixture has no internet routes. The complete Ohio
browser journey is a separate test of the released CloudFormation topology.

After collecting the evidence, generate and review a destroy plan for that
probe's own state. Verify the disposable instance, root volume, ENI, security
group, subnet and VPC are gone. Preserve the release's publisher-owned AMI and
snapshot. Keep customer account IDs, resource IDs and raw command outputs out of
public qualification records.

## Bootstrap release automation

Apply the separately reviewed [release automation Terraform](../terraform/publisher/release-automation)
once in the production publisher account. It creates the persistent GitHub OIDC
roles and phase boundaries, and a dedicated encrypted and versioned state
bucket in Ohio with all four S3 Block Public Access settings enabled. The
state bucket persists between releases. The operator retains its bootstrap
state separately from the disposable network state. Confirm the account has
the existing GitHub OIDC provider and EC2 Instance Connect service-linked
role before applying. Keep all customer and demo state separate.

Configure these inputs once for the protected workflow.

| GitHub scope | Variables and secrets |
| --- | --- |
| Repository variables | `SHIFTBOX_RELEASE_MAINTAINERS`, `SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID`, `SHIFTBOX_RELEASE_STATE_BUCKET`, and `SHIFTBOX_RELEASE_CLEANUP_ENABLED`. Set the cleanup flag only after verifying the cleanup environment and role. |
| `aws-release-candidate` | `SHIFTBOX_RELEASE_NETWORK_ROLE_ARN` from the bootstrap, the verified upstream Ohio `SHIFTBOX_RELEASE_SOURCE_AMI`, `SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY`, the template staging role and bucket map, ECS host AMI map, PostgreSQL digest, and the read-only settings token. The workflow generates the one-time SSH private key and keeps it on the build runner. |
| `aws-release-stable` | The stable verification role and read-only settings token. |
| `aws-release-cleanup` | `SHIFTBOX_RELEASE_NETWORK_CLEANUP_ROLE_ARN` from the bootstrap. Restrict deployments to protected main and allow unattended runs so the schedule can recover a lost runner. |

Use the existing reviewed tag rules and matching candidate and stable
deployment protections. The release verifier checks canonical version syntax,
maintainer authority, immutable releases, and exact source and evidence.
Confirm the reviewed Canonical source, guest public key, ECS host AMIs and
PostgreSQL digest before tags. Rotate those inputs when their upstream pins
change. The settings token requires repository Administration read and
belongs in each release environment.

### Rehearse the build network

Use **Rehearse publisher build network** on protected `main` with the full
commit SHA after Checks succeeds on that SHA. The `aws-release-network-rehearsal`
environment admits only `main` and requires the release maintainer's review.
Set `SHIFTBOX_RELEASE_NETWORK_REHEARSAL_ROLE_ARN` from the Terraform output
and set `SHIFTBOX_RELEASE_SOURCE_AMI` to the verified Canonical Ohio image in
that environment. The production account and state bucket are repository
variables. Configure and verify the protected `aws-release-cleanup` environment
and enable `SHIFTBOX_RELEASE_CLEANUP_ENABLED` before dispatch.

The single approved job provisions and validates the same Ohio Terraform
topology, then retires it even if provisioning fails. Its `rel-r` resources,
`10.98.1.0/24` CIDR and `release-builds/rehearsals/` state prefix isolate it
from release attempts. `v0.0.0-rc.1` is the reserved resource label. The job
has read-only GitHub repository access and its AWS role has no image build,
copy or sharing permissions. The normal expiry schedule and protected main
cleanup job handle a lost runner.

Verify the job's provision and retire steps, the exact encrypted retirement
marker, the empty Terraform state and the absence of the tagged test resources.
Preserve the state journal and existing release assets. A repeat rehearsal uses
a new dispatch so it receives a fresh state key. It does not qualify an RC.

Expand the failed step in GitHub Actions to see Terraform resource progress,
change counts and AWS error identifiers. Hosted Packer steps show their
checkpoints as the build runs. The public logs omit raw plans, Terraform
outputs, provider request details, and raw guest or Packer output.

## Run a protected release

Run Checks on the exact main SHA, then create the protected RC tag at that
commit. Approve the candidate setup job to reserve a private draft. The build
job's approval covers the leased Ohio Terraform plan, which the workflow
checks and applies before Packer starts. The only EC2 builder is private and
uses the scoped EC2 Instance Connect endpoint. The build saves the immutable
AMI handoff, closes its temporary AWS role, and retires the network and key.
Terraform uses an encrypted, locked state key for that GitHub run attempt.
The workflow keeps plans and state out of GitHub artifacts.

Approve regional copy after inspecting the Ohio handoff. The copy job opens a
separate temporary role for that AMI, freezes the regional set and retires
the role. Review the four exact AMIs before approving sharing. The regional
AMI sharing gate and public template bucket policy are separate publisher
access controls and require their existing authorization. The share job
requires EC2 DryRun success for all still-private AMIs before sharing any.
Recheck the public grants and restore the block on new sharing after the
reviewed publication window.

The normal job attempts cleanup after success or failure. The protected main
cleanup workflow independently reconciles expired attempts whose owning job
has ended. Dispatch it from main to retry a missed sweep. An AWS expiry
schedule also removes billable compute and egress if GitHub is unavailable.
If recovery reports residual resources or an unknown state, reconcile the
exact attempt against its private state and AWS inventory. Never apply a
destroy plan for a different attempt, another account, or a release artifact.

Rerun failed jobs on the existing protected run to preserve successful work.
If an earlier rerun froze a build or copy handoff, rerun the same workflow
and verify it reuses the same artifact IDs. Reconcile an AMI created before
its handoff uploaded before retrying. The first GHCR push can require a
separate package visibility approval. The workflow preserves the exact
payload so a protected rerun can verify anonymous pull and complete
publication.

Stable uses a protected annotated tag at the qualified RC commit. Include
one `candidate: <tag>` and one `qualification: <reviewed-main-commit>` line.
The stable workflow verifies the exact published artifacts and qualification
record before promotion.

The original protected `v1.0.0-rc.1` tag keeps its original release workflow.
Operate its remaining phases with the corresponding reviewed legacy role
and handoffs. Use the automated workflow from a later tag that contains
these changes.

Before authorizing Docs deployment, set the GitHub Pages publishing source to
**GitHub Actions** and set the `github-pages` environment deployment policy to
protected `main`. The Pages workflow runs independently from package
publication. Refresh it from verified `main` after promoting stable. Review the
landing page, installed-version guide and exact region links against the
published GitHub Release. Pages needs no AWS or package publication permissions.
