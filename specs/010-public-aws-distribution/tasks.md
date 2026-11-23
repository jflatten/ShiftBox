---
description: "Implementation tasks for the first public AWS distribution"
---

# Tasks: First Public AWS Distribution

**Input**: [Specification](spec.md), [plan](plan.md), [research](research.md), [data model](data-model.md), [installation contract](contracts/public-install.md), [publication contract](contracts/release-publication.md), [validation quickstart](quickstart.md)

**Prerequisites**: Approved implementation scope and the existing AWS provider. Repository settings, publishing and AWS effects require the appropriate maintainer/operator authorization during implementation.

**Tests**: Write the relevant unit tests first and observe the intended failure before changing code. Every test includes a descriptive comment. Retain existing acceptance gates and document actual integration evidence.

## Format

- Tasks use `- [ ] TNNN [P] [USn] Description with a repository-relative path`.
- `[P]` marks work that can run in a subagent after its listed prerequisites, up to five in parallel. Story labels apply only within story phases.
- Check off each task immediately when its completion criteria are met.

## Phase 1: Setup (Shared Infrastructure)

- [ ] T001 [P] Verify reviewed buildable main, CI, release ruleset/immutability availability, and package ownership/visibility in `platform/release/README.md`. Keep private operator details out of public docs.
- [ ] T002 [P] Verify the approved AWS publisher and its regional AMI, EBS and S3 Block Public Access controls, dedicated bucket ownership, Packer runner, OIDC roles, version retention and separate validation accounts. Record public prerequisites in `platform/packer/aws/README.md` and keep validation access details private.
- [X] T003 [P] Inventory the applicable V1–V14 evidence and current private deployment checks in `specs/010-public-aws-distribution/quickstart.md` and `documentation/validation/backup-recovery.md`. Confirm distinct shared demo and disposable qualification environments, cleanup ownership, and a reviewed path for missing or failed gates.

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Establish shared release payload and compatibility contracts before consuming or publishing them.

- [X] T004 [P] Add failing commented tests in `platform/release/release.test.sh` for invalid release/tag/region data, channels, identities, changed hashes, malformed S3 versions or URL encoding, wrong-region quick-create links and unsafe support archive layout.
- [X] T005 [P] Add failing build/runtime compatibility tests in `cmd/shiftbox/setup_test.go` and `platform/packer/aws/test-recipe.sh` for mismatched controller/guest build identities, continued development defaults and embedded UI/runtime parity.
- [X] T006 Define `platform/manifests/distribution.schema.json` and `platform/release/manifest.sh` against T004. Require schema version `1`, immutable candidate SHA/tag and runtime compatibility, an approved `linux/amd64` SHA-256 GHCR digest, fixed asset names and checksums, approved 12-digit publisher ID and pinned PostgreSQL 17 Debian digest. Require exactly one entry per `us-east-1`, `us-east-2`, `us-west-1` and `us-west-2` with host and AMI metadata, a region-matched quick-create URL, content-addressed object key, nonempty S3 version ID and matching template checksum.
- [X] T007 Use T005 to stamp the RC build identity consistently in `platform/container/Dockerfile`, `cmd/shiftbox/setup.go` and `platform/packer/aws/build-image.sh`. Keep the full Linux amd64 runtime and embedded assets, and keep release channel labels out of runtime compatibility comparisons.

**Checkpoint**: Install and release can consume test fixtures for the same pinned payload.

## Phase 3: User Story 1 - Install privately and open a workspace (Priority: P1)

**Goal**: Complete console quick-create or guided CLI private install and reach a signed-in IDE from released assets.

**Independent Test**: Use an official-format payload fixture and a disposable AWS install. Verify the exact hosted template, reviewed change sets, anonymous pull, safe resume, protected setup, private DNS and TLS, a validated exact catalog choice, profile/template persistence and IDE access.

### Tests for User Story 1

- [X] T008 [P] [US1] Add failing commented cases in `scripts/install-aws.test.sh` for stable/RC/pin/absent stable, truncated stdin, wrong template, account/region/provenance, absent TTY, denied IAM capability, stale/rejected change sets, interrupted stack/operation identity, wrong CA and private route failures.
- [X] T009 [P] [US1] Add failing commented cfn-guard and template fixtures in `platform/cloudformation/aws/tests/guard/` and `platform/cloudformation/aws/template.test.sh` for identical controller/setup digests, default closed startup, scoped roles and secret references, exact AMI and owner, HTTPS/network boundaries, encrypted storage, retention, and unauthorized replacement or premature start.
- [X] T010 [P] [US1] Add failing commented cases in `platform/cloudformation/aws/setup.test.sh` for wrong outputs and stack identity, ambiguous setup tasks or lost response, insufficient privilege, interrupted wizard and failure to stop a setup task.
- [X] T011 [P] [US1] Add failing commented Go tests in `internal/provider/ec2/provider_test.go`, `internal/release/ec2_test.go` and `cmd/shiftbox/cloud_provider_test.go` for exact official owner/region/root/lineage, missing or deprecated image, unavailable publisher tags, arbitrary public AMI rejection, private legacy records, consistent canary and actual enrollment, and encrypted launched roots.
- [X] T012 [P] [US1] Add failing permission, validation/progress and save/reload checks in `internal/app/platform_import_test.go`, `ui/features/admin/infrastructure.test.tsx` and `ui/tests/e2e/mvp-workspace.spec.ts` for the configured official release and exact pinned first workspace. Include unauthorized import, invalid provenance and catalog canary failure.

### Implementation for User Story 1

- [X] T013 [US1] Create standalone public CloudFormation YAML in `platform/cloudformation/aws/template.yaml`. Match the private ECS-on-EC2 topology, stable encrypted EBS/AZ, scoped networking, PostgreSQL, and shared pinned GHCR digest for setup/controller. Reuse tested scripts and use vetted regional mappings for the host and official workspace AMIs. Keep the internal root in `platform/terraform/aws/` for the demo.
- [X] T014 [US1] Implement and test `platform/cloudformation/aws/stack-policy.json`, `platform/cloudformation/aws/shiftbox.guard` and guarded defaults in `platform/cloudformation/aws/template.yaml`. Require stable logical IDs, Retain on deletion/replacement for EBS and managed secrets, exact official image/publisher IAM and persona-specific roles. Verify stack policy, termination protection, IAM capabilities, and cfn-lint, cfn-guard and invalid fixtures from T009.
- [X] T015 [US1] Add foundation, database/setup and active phases in `platform/cloudformation/aws/template.yaml`, preserving the same volume/secrets. Generate independent scoped role passwords, take only secret references/public TLS parameters, keep controller stopped until protected activation, and scope optional Tailscale and existing private access.
- [X] T016 [US1] Implement the CloudFormation stack-output setup entry in `platform/cloudformation/aws/setup.sh`, reusing compatible code from `platform/terraform/aws/setup.sh`. Reconcile one authorized task, verify persisted activation and task stop through existing protected SSM/browser setup, and confirm controller startup fails closed if setup is incomplete. Pass T010.
- [X] T017 [US1] Implement official descriptor validation in `internal/config/config.go`, `internal/provider/ec2/provider.go`, `cmd/shiftbox/cloud_provider.go` and `internal/release/ec2.go`. Require exact region, AMI/owner, root device/snapshot, lineage, guest identity, minimum disk and catalog asset path/hash. Verify amd64/HVM/EBS/public state, preserve private legacy and encrypted launched roots, use consistent proven enrollment, and pass T011.
- [ ] T018 [US1] Extend the existing operation in `internal/app/platform_import.go` and add the official release control in `ui/features/admin/ami-families.tsx`. Reuse admin permission, canary progress and cleanup, fail visibly, and hand off to the exact pinned resource profile and template. Verify with T012 and apply the repository's Mantine, Carbon and branding guidance.
- [X] T019 [US1] Implement `scripts/install-aws.sh` with version checks and official asset verification, expected AWS account, region, stack name/ID, IAM acknowledgement and a local lock. Reconcile stack/review and operation IDs, present pre-deployment findings from `describe-events` and exact CREATE/UPDATE change sets, require explicit execution approval for each, and pass T008.
- [X] T020 [US1] Complete guided and manual/console steps in `scripts/install-aws.sh` and `platform/cloudformation/aws/README.md`. Guide scoped DB TLS writes, private DNS and verified browser issuer/leaf flow, Tailscale or existing route, stack outputs, protected SSM wizard and GitHub App/owner authorization. Require change-set review and preserve scoped references and installation resources.
- [ ] T021 [US1] Run console, manual and CLI fixture journeys using `specs/010-public-aws-distribution/quickstart.md`. Check protected stack lifecycle, same-ID resume, change-set rejection, wrong-region/identity/pin, IAM, bad DNS/CA and profile/template persistence.

**Checkpoint**: Install through the IDE works from a prepared public-format payload. Live public qualification still uses the exact RC.

## Phase 4: User Story 2 - Publish and qualify an exact release (Priority: P2)

**Goal**: Make a version-tagged prerelease, then verify and promote its exact tested artifacts.

**Independent Test**: A rehearsal repo rejects invalid tags and partial publication; a valid fixture candidate and evidence produce stable with identical template bytes and four S3 versions, unchanged OCI digest and the same four AMI IDs.

### Tests for User Story 2

- [X] T022 [P] [US2] Add failing commented publication and promotion tests in `platform/release/release.test.sh` for protected tag and reviewed source, candidate/evidence mismatch, failed anonymous pull, altered/missing S3 bytes or versions, wrong region, withdrawn/deprecated AMI, partial publication and default stable regression.
- [X] T023 [P] [US2] Add failing public-mode and metadata tests in `platform/packer/aws/public-recipe.test.sh` for encrypted/default-encryption or foreign source rejection, clean sealing, regional copy provenance and exact share targets. Include preservation of the private builder path.
- [X] T024 [P] [US2] Add failing commented workflow, template and archive contract tests in `platform/release/workflows.test.sh` for tag-only writes, role separation, read-only CI, immutable draft-to-public transition, regional S3 URL reuse, byte-identical template and exclusion of private files.

### Implementation for User Story 2

- [X] T025 [US2] Implement the public build/seal path in `platform/packer/aws/shiftbox.pkr.hcl`, `platform/packer/aws/build-image.sh` and `platform/packer/aws/seal-image.sh`. Use the approved publisher context and pinned clean unencrypted source, verify scrubbed output and keep existing private mode. Pass T023.
- [X] T026 [US2] Implement `platform/release/bundle.sh` and `platform/release/build-candidate.sh` to stage the standalone template, support archive and checksummed assets. Validate allowed tags `vMAJOR.MINOR.PATCH` or `vMAJOR.MINOR.PATCH-rc.N` with no leading zeros and positive N. Render one template from vetted regional pins, support catalog metadata and include matching stack policy, the stack-output setup helper and public guides.
- [X] T027 [US2] Implement regional copy/share in `platform/release/amis.sh`, template upload/verification in `platform/release/templates.sh` and Terraform-managed scoped roles and four dedicated release buckets in `platform/terraform/publisher/main.tf`. Verify four exact clean AMIs and same template bytes. Use SSE-S3, ownership enforced, ACL blocks, versioning and release-only anonymous version reads. Protect referenced versions from deletion and ensure unsigned list/write and unrelated access fail.
- [X] T028 [US2] Implement tag and draft checks in `platform/release/publish.sh` and `.github/workflows/release.yml`. Reuse `.github/workflows/ci.yml` baseline checks including cfn-lint and cfn-guard, verify unsigned GHCR and S3 access, generate console links/checksum notes, and reconcile retries without overwriting staged assets.
- [X] T029 [US2] Implement `platform/manifests/qualification.schema.json` and independent verification in `platform/release/promote.sh`. Require exact RC source/release/assets IDs and hashes, four results with hosted-template validation and cross-account AMI boots, fresh Ohio console IDE, CLI rehearsal, existing gate passes and cleanup. Compare all four S3 versions and bytes, copy exact assets into the stable GitHub Release without new S3 uploads or rebuild, and preserve stable on rejection or older retry.
- [X] T030 [US2] Add failing commented mock assertions in `platform/terraform/publisher/tests/public-ami-launch/tests/launch.tftest.hcl` before implementing the separately scoped cross-account disposable AMI fixture in `platform/terraform/publisher/tests/public-ami-launch/main.tf`. Document encrypted private boot and cleanup in `platform/release/README.md`.
- [ ] T031 [US2] Rehearse valid/invalid RC and stable events, ruleset enforcement, partial S3 uploads, object read isolation and same-byte retries with `.github/workflows/release.yml` and the `specs/010-public-aws-distribution/quickstart.md`. Verify AWS and GitHub publisher settings and cross-region pre-deployment validation.
- [X] T032 [US2] After authorized main preparation and settings review, publish the complete official RC by protected tag through `.github/workflows/release.yml`. Verify source, anonymous GHCR and versioned S3 reachability, template asset/checksum, all four Console links and AMI pins. Record the operational path in `platform/release/README.md`.
- [ ] T033 [US2] Qualify the exact RC with four cross-account public AMI boots and hosted-template change-set validations, a fresh Ohio console IDE install, the CLI change-set/resume path and private-route evidence in `specs/010-public-aws-distribution/quickstart.md`. Verify teardown of stacks and separately retained test resources. Submit sanitized record to `platform/manifests/qualification/<rc-tag>.json` via protected main.

**Checkpoint**: The RC is public and its exact payload has verified promotion evidence. Stable publication follows the final launch checks.

## Phase 5: User Story 3 - Find the right public instructions (Priority: P3)

**Goal**: Publish a navigable Pages entry with correct stable, explicit release and source paths.

**Independent Test**: Render Pages from dated fixtures with and without stable. Follow install and version links. A docs-only publication rehearsal makes no release changes.

### Tests for User Story 3

- [X] T034 [P] [US3] Add failing commented site fixtures in `docs/site/tests/site.test.sh` for repository subpath, stable absent, explicit RC/version, release-backed regional launch URLs, stale or inaccessible manifest and private content exclusion.
- [X] T035 [P] [US3] Add failing Pages isolation checks in `platform/release/pages-workflow.test.sh` for branch-scoped Pages publishing, PR build-only checks and absence of package, release or AWS write privileges.

### Implementation for User Story 3

- [X] T036 [US3] Add locked Jekyll build in `docs/site/Gemfile`, `docs/site/Gemfile.lock`, `docs/site/_config.yml` and `docs/site/_layouts/default.html`. Render only verified manifest links from the selected release, verify region/encoding and fallback to canonical release links if manifest results are unavailable. Pass T034.
- [X] T037 [US3] Write `docs/site/{index,install,private-access,troubleshooting,contributing}.md` and update `README.md` and `platform/release/release-notes.md`. Cover four region quick-create links, template and checksum inspection, CLI and SSM setup, first workspace, private DNS/port/trust and source build. Link the upgrade/recovery guide and explicit RC/pin instructions.
- [X] T038 [US3] Implement `.github/workflows/pages.yml` with PR validation, protected-main release metadata refresh and isolated Pages publication. Rehearse release-independent docs updates and verify the approved Pages settings procedure in `platform/release/README.md`.

**Checkpoint**: Public docs are independently buildable and publishable, and version selection remains release-owned.

## Phase 6: User Story 4 - Plan a safe upgrade or recovery (Priority: P4)

**Goal**: Give an operator an actionable documented update and recovery with the existing product safeguards.

**Independent Test**: Check the guide's commands against the AWS backup and restore evidence, exercise recovery on a same-release disposable installation and review failure and first-release paths.

### Tests for User Story 4

- [X] T039 [P] [US4] Add commented guide assertions in `docs/site/tests/recovery-guide.test.sh` for pre-migration backup verification, retained-workspace gate, reviewed change sets, storage continuity, matching-release recovery and review before authority resumes.

### Implementation for User Story 4

- [X] T040 [US4] Write `docs/site/upgrade-recovery.md` from `documentation/validation/backup-recovery.md` and architecture references. Include compatibility, stack policy and same-volume review, verified app/Dex/config/key backup, change-set validation before updates, safe migration and failed migration recovery. Document the first-stable and future public upgrade rehearsals.
- [ ] T041 [US4] Walk the upgrade procedure and verify same-installation AWS restore with the current candidate on a disposable PostgreSQL target through `documentation/validation/backup-recovery.md` and `specs/010-public-aws-distribution/quickstart.md`. Verify bad-archive/key failures, administrator reconciliation and cleanup. Keep the first stable's public-to-public transition outside qualification until a predecessor exists.

**Checkpoint**: Documented recovery and existing gates are verified through the current distribution.

## Phase 7: Polish & Cross-Cutting Concerns

- [ ] T042 Run release baseline, end-to-end private checks and regression gates in `.github/workflows/ci.yml` and `specs/010-public-aws-distribution/quickstart.md`. Verify existing internal Terraform/ECR and UTM behavior, browser permissions and demo integration used during implementation. Reconcile and clean any test-only resources.
- [ ] T043 Review the candidate site, all four scoped S3 links, qualified assets, operator guide, reviewed main, tag protections and exact evidence via `platform/release/README.md` and `platform/manifests/qualification/<rc-tag>.json`. Ensure review and deployment artifacts disclose no private test data.
- [ ] T044 With authorized maintainer approval, push the protected stable tag for the exact qualified commit. Verify `.github/workflows/release.yml` reuses template bytes, original S3 versions/URLs, GHCR digest and AMIs. Verify default and explicit release links using `specs/010-public-aws-distribution/quickstart.md`.
- [ ] T045 Refresh and verify Pages stable and pinned regional quick-create links independently through `.github/workflows/pages.yml` and `docs/site/tests/site.test.sh`. Record review URLs and private teardown evidence, preserving published S3 object versions and other assets still referenced.

## Phase 8: Workflow-managed publisher build

- [X] T046 [P] [US2] Add one-time Terraform bootstrap for the private encrypted production state backend and least-privilege publication and recovery identities. Test bucket protection, role trust and permission boundaries offline in `platform/terraform/publisher/release-automation/`.
- [X] T047 [P] [US2] Implement the exact-run Ohio network lease, private Terraform state, approved plan application, one-time SSH handoff and exact-state teardown in `platform/packer/aws/`. Generalize the candidate input while preserving the existing nonpublishing rehearsal path. Test retries, account and ownership guards, partial failure and teardown.
- [X] T048 [P] [US2] Implement independent cleanup for interrupted release builds. Use the exact run and durable state on protected main with a cleanup-only role. Verify cancellation, runner loss, live-run refusal, stale locks and preservation of publication artifacts.
- [X] T049 [US2] Wire the protected publication job to the network lifecycle and scope AMI policy phases to the exact approved run and artifacts. Preserve tag and publication approvals, reuse resumable handoffs and eliminate per-version manual Terraform commands after one-time setup.
- [ ] T050 [US2] Verify the complete automated release lifecycle using offline fixtures and a separately approved nonpublishing check. Document bootstrap, recovery and maintainer operation in `platform/release/README.md`. Confirm each temporary network is absent and release assets remain available.

## Dependencies & Execution Order

### Phase Dependencies

- Phase 1 → Phase 2 → story unit tests and implementations. T006 depends on T004. T007 depends on T005.
- Use fixtures for T021 and the initial US2/US3 rehearsals. T026 can develop against the prescribed template and archive layout with fixtures; verify real packaged content before T032.
- T031 depends on T025–T030. T032 requires T021, T031, T036–T040 and buildable reviewed main containing the candidate's final code, installer and docs.
- T033 starts after T032. Complete T041 and T042 before T033 submits the record. Qualification requires passing applicable gates and verified cleanup. Then T043 → T044 → T045 for launch.

### User Story Dependencies

| Story | Independent increment | Release dependency | Requirements |
| --- | --- | --- | --- |
| US1 | Guide an authenticated operator to an IDE with a verified public-format fixture. | Real RC input is required for T033. | FR-001–007 |
| US2 | Verify candidate and promotion rules with a rehearsal repo and fixture evidence. | Genuine stable requires completed US1, US3, US4 and exact public RC evidence. | FR-001–003, FR-008–011 |
| US3 | Render public guides and exercise links with no-stable/stable/RC fixtures. | Bundle completed version docs before RC; verify live stable links after promotion. | FR-004, FR-006, FR-012 |
| US4 | Check guide and rerun existing backup/restore gates using a disposable matching release. | Include AWS recovery outcomes in qualification. | FR-013 |

### Within Each User Story

- Write and observe failing commented tests before code. Use the listed tests to protect the legacy path and fix only failures within the approved scope.
- Validate the story in isolation before it enters the launch gate. Check off implementation tasks immediately when complete.

### Parallel Opportunities

- T001–T003 and T004–T005 can run in their respective phases on separate files.
- T008–T012 can run together. T023/T024 can run alongside T022 after the foundational release test file is ready.
- T034/T035 and T039 can run in parallel after foundation. Coordinate T037/T040 on cross-links and publish the website only under its dedicated workflow.

## Parallel Examples by User Story

- **US1**: Use separate agents for installer fixtures (T008), CloudFormation guard and template fixtures (T009), setup recovery (T010), AMI admission (T011), and catalog/browser (T012).
- **US2**: Run the Packer and workflow contract tests (T023/T024) alongside tag policy and promotion cases (T022).
- **US3**: Build link/privacy fixtures (T034) alongside Pages workflow checks (T035).
- **US4**: Write recovery-guide assertions (T039) alongside US3's independent fixtures. Run actual recovery against the candidate with the controlled AWS validation window.

## Implementation Strategy

### MVP First (User Story 1)

Complete foundation, then prove a console and CLI fixture install through the first workspace and exercise the pinned manual path. Use the private AWS demo for permitted regression work. This increment is independently testable. Public stable requires the remaining stories and exact qualification.

### Incremental Delivery

1. Establish release contracts and validate installation from fixtures.
2. Rehearse the publication pipeline, then complete public guides and package the reviewed source.
3. Publish a candidate, qualify it in all four regions and through the fresh Ohio browser install, and capture recovery and cleanup evidence.
4. Independently verify the stable tag and promote the same artifacts, then verify the independent Pages entry.

### Parallel Team Strategy

After foundation, run independent story tests and docs preparation in subagents. Keep work sharing runtime, manifest, infrastructure or workflow files ordered. Use a single publication mutation lock for live release jobs.

## Notes

- Follow existing product acceptance gates and the AWS-first sequencing decision. Keep reference architecture immutable and the existing UTM flow supported by its current contract.
- Production settings changes, public tagging and release operations require separate review. Keep AWS qualification and site publication pending until their approved prerequisites and evidence are available.
