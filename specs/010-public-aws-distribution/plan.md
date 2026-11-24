# Implementation Plan: First Public AWS Distribution

**Branch**: Spec Kit feature `010-public-aws-distribution`, detached at `feature/mvp` commit `48f4d92` | **Date**: 2026-11-21 | **Spec**: [First Public AWS Distribution](spec.md)

**Input**: [Feature specification](spec.md)

## Summary

Distribute the existing single-controller private AWS deployment through GitHub Releases. Ship the full amd64 controller at an anonymous GHCR digest and a checksummed CloudFormation template with a Bash installer and regional AWS Console launch links. Pin a clean official workspace AMI in each of four supported regions. Guide operators through CloudFormation quick-create or reviewed CLI change sets, ECS/SSM bootstrap, private HTTPS, and the existing catalog and first-workspace flow. Use an explicit qualification record to promote the identical public RC payload to stable. Publish public guides through an independent GitHub Pages workflow.

## Technical Context

**Language/Version**: Go and Node pinned by `go.mod` and existing CI (Go 1.27.4 and Node 26.13.0 at this checkout), Bash with a macOS-compatible 3.2 baseline, CloudFormation YAML, Packer 1.14.2 and its pinned AWS plugin, and a locked Jekyll site build.

**Primary Dependencies**: AWS CLI v2, Session Manager plugin, curl, jq, OpenSSL with the required certificate extensions, GitHub Actions / Releases / GHCR / Pages, and Packer. Use cfn-lint and cfn-guard in CI and Terraform for internal infrastructure. Operators use normal AWS access. Select existing private service and workspace subnets. The operator provides private connectivity and DNS forwarding, including when using Tailscale.

**Storage**: CloudFormation stack state and retained, separately owned EBS and Secrets Manager resources in the customer's account. The CLI keeps only nonsecret resume metadata in an operator-only directory. Existing encrypted ECS host data volume for the ShiftBox installation and PostgreSQL 17 data, scoped Secrets Manager values, encrypted workspace roots per EC2 instance. Publication metadata lives in release assets and one reviewed qualification file per RC. Keep full evidence in existing restricted operator storage.

**Testing**: Commented unit tests before implementation. Reuse `make fmt-check vet test race`, `make ui-test build`, embedded UI tests, the repository's browser and integration suites, cfn-lint and cfn-guard checks, CloudFormation pre-deployment and change-set validation, internal Terraform regression tests, Packer recipe checks and temporary-directory shell fixtures. Use the private demo for permitted integration checks and separate disposable deployment accounts for public qualification.

**Target Platform**: AWS commercial partition, `us-east-1`, `us-east-2`, `us-west-1`, `us-west-2`; ECS on an EC2 amd64 host and Ubuntu 24.04 Minimal amd64 workspace AMIs. Validate the install frontend from macOS and Linux terminals.

**Project Type**: Release automation, CloudFormation and shell distribution, a narrow change to EC2 image admission and admin onboarding, and a static documentation site.

**Performance Goals**: One fresh Ohio install reaches the IDE with no customer builds. Four of four exact images launch cross-account. No timing SLA or new capacity target is planned.

**Constraints**: No public browser access, registry credentials for public pulls, or unreviewed provisioning. Preserve encrypted deployment data, scoped identity/DB/guest credentials, provider reconciliation, persistent state and the UTM path. The first stable has no earlier public stable. Planning ends at task generation.

**Scale/Scope**: One AWS topology, one public OCI package lineage, one immutable CloudFormation template per RC hosted in four regional S3 buckets and promoted unchanged, four regional workspace images, one docs site and the existing product acceptance gates.

## Constitution Check

| Gate | Before research | After Phase 1 design |
| --- | --- | --- |
| I. Simple design | Translate the current AWS deployment topology into one native template and reuse runtime/bootstrap assets, Packer and release surfaces. | Pass. Use one template, release manifest, reviewed evidence record and Bash entry point. Regional copies have identical bytes. A static Jekyll build serves the requested guides. |
| II. Explicit trust | Preserve provider and guest ownership, private origins and scoped cross-account permissions. | Pass. Pin template object versions, release assets and live AMI owner, bind setup to ECS/SSM, and keep browser issuer, database trust and guest authority separate. |
| III. Durable work | Preserve installation and workspace data and reconcile before retry. | Pass. Use reviewed stack changes, protected retained resources, AWS setup inventory and the existing backup, migration and restore contract. |
| IV. Test first | Require commented unit tests and real failure evidence. | Pass. Use test-first phases and keep demo checks separate from exact public RC qualification. |
| V. Visible workflows | Reuse the protected browser setup and existing catalog UI. | Pass. Use Mantine Admin controls, reuse validation progress and verify permissions, save/reload and first workspace end to end. |
| Architecture sequencing | The user explicitly approved AWS as the first public distribution, replacing the older UTM-first sequencing for this feature. | Pass under that narrow approval. The shared data, trust and recovery obligations continue, with AWS-specific V1/V2/V5/V10 validation and a UTM regression smoke. The user also explicitly approved CloudFormation for customer-facing stacks, with internal Terraform for demo and publisher. Architecture references and constitution text remain unchanged in this planning work. |

## Project Structure

### Documentation (this feature)

```text
specs/010-public-aws-distribution/
├── spec.md
├── checklists/requirements.md
├── plan.md
├── research.md
├── data-model.md
├── contracts/
│   ├── public-install.md
│   └── release-publication.md
├── quickstart.md
└── tasks.md
```

### Source Code (repository root)

```text
.github/workflows/{ci,release,pages}.yml
README.md
docs/site/                              # Public page sources, generator lock and checks
platform/container/Dockerfile
platform/manifests/                     # Existing guest manifest plus distribution/qualification
platform/packer/aws/                    # Existing recipe, build, sealing and recipe tests
platform/release/                       # Version, bundle and publication helpers with tests
platform/cloudformation/aws/            # Standalone public template, stack policy, Guard rules and fixtures
platform/terraform/aws/                 # Internal/demo deployment and bootstrap source to reuse
platform/terraform/publisher/           # Scoped publisher roles and four release-only S3 buckets
scripts/install-aws.sh                  # Published as install.sh, with shell fixture tests
cmd/shiftbox/{cloud_provider,setup}.go
internal/{app,config,provider/ec2,release,web}/
ui/features/{admin,setup}/
ui/tests/e2e/
documentation/validation/backup-recovery.md
```

**Structure Decision**: Keep AWS runtime, release tooling and guides in their existing or purpose-specific directories. Render a standalone CloudFormation template from the public deployment definition and reuse tested bootstrap assets where practical. The public release manifest is separate from `platform/manifests/release.json`, which already serves product release/guest contracts. The publisher root manages release-specific IAM, S3 buckets and any needed build prerequisites through Terraform. It does not manage customer or demo state.

## Phase 0: Research

[Research decisions and sources](research.md) resolve the public image and enrollment mismatch, current bootstrap prerequisites, script hosting, TLS scope, publisher permissions, stable promotion, source availability and docs build. Publisher account, package visibility, tag rules and AWS/GitHub roles are prerequisites to verify during authorized implementation. No existing account or configuration is assumed to have those permissions.

## Phase 1: Design and Contracts

- [Data model](data-model.md) defines the payload, regional S3 objects, qualification evidence, stack identity and trust boundaries.
- [Public installation contract](contracts/public-install.md) defines quick-create links, version selection, change-set review, resume, private access and first-workspace steps.
- [Release publication contract](contracts/release-publication.md) defines exact artifact matching, tag/evidence checks, recovery of incomplete publication and Pages isolation.
- [Validation quickstart](quickstart.md) traces the exact RC and existing gates from preflight to teardown. It is a future procedure and evidence template.

### Decisions for implementation

1. **Public deployment.** Publish one standalone CloudFormation YAML template for the existing ECS-on-EC2 and persistent PostgreSQL topology. Render exact digest and regional host/workspace pins in the template. Implement an explicit official AMI IAM allowance. Preserve internal Terraform and private ECR. Host identical bytes in four dedicated S3 buckets using versioned URLs, and attach an inspection copy/checksum to GitHub Releases. Verify all regional URLs and Console links. Public S3 retrieval is limited to versioned release template objects.
2. **Official catalog.** Pin the publisher and regional descriptor in validated config. Add a discoverable action in **Administration → Infrastructure → Platform** for that release and reuse the admin import operation, its canary, Stepper and cleanup. The associated pinned profile and template should be discoverable and persist after reload. Release inputs identify the exact image even when newer family members exist. Require consistent, qualified single-boot enrollment for the canary and workspace, including existing stopped-instance recovery checks. Preserve legacy private image and family admission.
3. **Guided setup.** Start with foundation and normal controller disabled, populate scoped DB TLS references and then review database/setup and active stages. Require the IAM capability acknowledgement and protect retained EBS/secret resources through retention attributes, stack policy and termination protection. Console operators inspect the regional launch template and use stack outputs plus documented local SSM/browser steps. The CLI shows validation events and actual changes before executing each approved change set. Reconcile stack, operation and setup task identities on retry. Keep secrets out of templates, parameters, change sets, outputs and normal logs.
4. **Release identity.** Build once per RC with identical controller/agent compatibility identity. Stable has its own public Git tag and release notes. Retain the candidate compatibility version and payload manifest bytes. Use a reviewed record on main and identify that commit in the stable tag annotation so the release workflow can independently read and verify it. Verify all hosted template object versions, availability and same-byte GitHub assets before public commit under release concurrency controls.
5. **Launch readiness.** Reviewed main needs buildable source and workflows before a tag can qualify. A maintainer later verifies GitHub protection, immutable releases, anonymous GHCR, publisher account and least-privilege roles. Qualification uses clean customer-like accounts, retains test evidence privately and cleans up test resources. The demo remains a distinct testbed with its existing state, access and ECR paths.
6. **Public docs and updates.** Serve rolling stable guides, explicitly link pinned/RC releases and attach matching Markdown guides to immutable releases. Use an allowlisted docs workflow with separate deploy permissions. Link the operator recovery guide to the current backup/migration and administrator review gates and exercise the same-installation disposable recovery path.

## Complexity Tracking

| Approved difference from older architecture | Reason | Boundary |
| --- | --- | --- |
| AWS precedes UTM for the supported public distribution. | The user explicitly selected AWS-first based on the existing private deployment. | Reuse the AWS provider and preserve UTM capability. Validate equivalent provider-specific release gates without rewriting the reference design. |
| Customer-facing AWS provisioning uses CloudFormation. | The user chose native console launch and CLI change sets so customers can start without installing Terraform. | Internal Terraform continues for the demo and publisher. Match the existing private topology, bootstrap and persistence contract. |
