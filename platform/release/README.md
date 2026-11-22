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

## Configure the staged release

Review the publisher Terraform plan and the existing private Packer runner before
the first protected tag. Keep Terraform state, Packer inputs, build SSH material,
and qualification logs out of the repository and the release.

Set the maintainer list, publisher account and runner label as repository
variables so preflight and runner selection can read them. Keep each role,
runner-local input and settings token scoped to its protected release
environment.

- `SHIFTBOX_RELEASE_MAINTAINERS` is the approved comma-separated maintainer
  GitHub login list. Confirm effective creation restrictions and independent
  update/deletion protection for version tags.
- `SHIFTBOX_RELEASE_SETTINGS_TOKEN` is an authorized read-only repository
  administration credential for verifying protected tag rules and release
  immutability. Grant it only to the protected release jobs that read settings.
- `SHIFTBOX_RELEASE_PUBLISHER_ACCOUNT_ID` identifies the approved AWS publisher.
  Configure `SHIFTBOX_RELEASE_AMI_ROLE_ARN` for the separately reviewed private
  Packer/copy/share role. Set `SHIFTBOX_RELEASE_TEMPLATE_ROLE_ARN` and
  `SHIFTBOX_RELEASE_VERIFY_ROLE_ARN` to the publisher module's candidate/stable
  role outputs. Use exact environment OIDC subjects and tag restrictions.
- Set `SHIFTBOX_RELEASE_CANDIDATE_RUNNER` to the approved runner label. The
  runner needs AWS CLI, Docker Buildx, Go, Packer, cfn-lint and cfn-guard. Set
  `SHIFTBOX_RELEASE_PACKER_INPUTS_PATH` to the runner-local `.pkrvars.json` path
  and `SHIFTBOX_RELEASE_GUEST_SIGNING_PUBLIC_KEY_PATH` to the runner-local
  32-byte Ed25519 public key path. Set
  `SHIFTBOX_RELEASE_TEMPLATE_BUCKETS` from the Terraform outputs,
  `SHIFTBOX_RELEASE_HOST_AMIS` to the vetted regional ECS AL2023 pins, and
  `SHIFTBOX_RELEASE_POSTGRES_IMAGE` to the approved PostgreSQL 17 Debian digest.

Require a successful baseline CI run on the exact reviewed tag commit. If that
commit is at the tip of `main`, run `gh workflow run ci.yml --ref main` and
verify the run succeeded on the intended SHA before pushing the tag. For an
existing release tag, dispatch Checks with `--ref <version-tag>` and rerun the
release workflow once Checks passes.

Only the protected tag starts a public candidate. Stable uses a protected
annotated tag on that same commit. Include one `candidate: <tag>` and one
`qualification: <reviewed-main-commit>` line. Keep the full verification
evidence private and use the minimal reviewed qualification record from the
[publication contract](../../specs/010-public-aws-distribution/contracts/release-publication.md).

The workflow reserves a private GitHub draft before candidate builds. A rerun
with the same workflow run ID can restore its exact staged Actions artifact and
upload missing assets to the draft. On the first GHCR push, GitHub can create a
private package. The workflow freezes the payload before checking anonymous
pulls. After a maintainer separately approves package visibility, rerun the
same workflow so the existing digest and assets can pass the anonymous checks.
If the staged artifact is unavailable and an
existing reservation or draft remains, the workflow stops so a maintainer can
reconcile the assets already created. If it cannot recover the exact staged
payload, use a new RC and preserve the existing release record. Complete the
approved maintainer and publisher rehearsal before using these environments.

Before authorizing Docs deployment, set the GitHub Pages publishing source to
**GitHub Actions** and set the `github-pages` environment deployment policy to
protected `main`. The Pages workflow runs independently from package
publication. Refresh it from verified `main` after promoting stable. Review the
landing page, installed-version guide and exact region links against the
published GitHub Release. Pages needs no AWS or package publication permissions.
