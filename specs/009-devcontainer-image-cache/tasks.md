---
description: Dependency-ordered implementation tasks for the project devcontainer image cache
---

# Tasks: Project devcontainer image cache

**Input** — [Spec](spec.md), [plan](plan.md), [research](research.md), [data model](data-model.md), [contracts](contracts/built-image-cache.md), and [functional validation guide](quickstart.md).

**Tests** — Write the commented tests before their corresponding implementation and confirm the expected failures. The checks cover function, authorization, and recovery. The launch-speed benchmark is outside scope.

## Format and paths

`[ID] [P?] [Story?] Description`. Paths are relative to the repository root. Run each ready `[P]` task in its own subagent, in batches of at most five. Check off tasks as soon as their work and verification are complete.

## Phase 1: Setup (shared infrastructure)

**Purpose** — Establish what the existing pinned CLI, Docker, and containerd paths can support before implementing the cache.

- [X] T001 [P] Write a commented pinned Dev Container CLI proof in `tests/release/devcontainer_image_cache_test.go` using `tests/release/devcontainer_fixtures_test.go`. Exercise Dockerfile and image-plus-Features output capture before UID adaptation, digest reuse, config bytes and metadata parity, Feature policy, initializer behavior, retained-home recreation, lifecycle hooks, and usable IDE.
- [X] T002 [P] Write a commented pinned Docker/containerd transport proof in `tests/release/built_image_registry_test.go` using the versions in `platform/packer/install-toolchain.sh`. Observe the single-platform push and digest pull method and size sequence, direct private namespace routing, and guest mTLS rotation. Verify the public pull-through route stays read-only.
- [X] T003 Exercise both proofs with a disposable fixture on `shiftbox-demo`. Record the usable CLI seam, OCI request sequence, failure behavior, and exact pins in `specs/009-devcontainer-image-cache/research.md`. Resolve discrepancies against the approved CLI and transport requirements before proceeding. Clean up proof resources.

## Phase 2: Foundation (blocks all stories)

**Purpose** — Establish durable cache state, shared storage admission, and current source and workspace authorization.

- [X] T004 [P] Add commented schema tests in `tests/contract/database_migrations_test.go` for both database dialects. Cover the new MVP **On** default, policy generations and purge cutoff, a unique project/platform slot, and bounded attempt state.
- [X] T005 [P] Add commented storage tests in `internal/imagecache/built_storage_test.go` for admission before bytes are written, invalid length or digest, partial uploads, shared-digest accounting, incomplete or evicted image closures, active leases, and preservation of public content during a built-content purge.
- [X] T006 [P] Add commented GitHub origin tests in `internal/identity/github_origin_test.go` for exact config bytes, current owner GitHub authorization, existing default/branch/tag/commit resolution, branch-before-tag precedence, and denied, unavailable, or unsafe source reads.
- [X] T007 [P] Add commented authority tests in `internal/app/built_image_authority_test.go` for a second project using identical source, revoked membership or source access, stale generation or dispatch, and an eligible upload finishing after a successful operation.
- [X] T008 Add equivalent migrations in `migrations/sqlite/00043_built_image_cache.sql` and `migrations/postgres/00043_built_image_cache.sql` and register them in `internal/database/migrate.go`. Use the next free prefix if another change takes 00043. Persist policy version, publication generation, and oldest pending purge cutoff. Key current pointers by installation, project, target OS, and architecture so the tuple “has at most one visible image.” Store canonical source, selected path, exact-config SHA-256, manifest digest, storage generation and location, and publication metadata. Tie upload attempts to their expiry, policy generation, operation, and dispatch.
- [X] T009 [P] Add built-image staging, closure checks, leases, and targeted reclamation in `internal/imagecache/built_storage.go` and `internal/imagecache/storage.go`. Apply the existing shared budget and eviction rules to staging bytes, reservations, manifest and namespace links, and digest-addressed content. Reconstruct usage after restart. Release expired staging and preserve active leases and public references.
- [X] T010 [P] Add bounded authorized project-config lookup in `internal/identity/github_origin.go` and `internal/identity/github_oauth.go`. Resolve current ref using the guest’s established precedence, pin the config read to that revision, and return the exact-byte digest and authorization version. Keep tokens and config contents out of cache state and logs. Return a decline for unverifiable source.
- [X] T011 Add shared cache-grant authority in `internal/app/built_image_authority.go`, reusing the connection registry in `internal/proxy/registry.go`. Derive installation, owner, project, platform, operation, dispatch, and source from authenticated guest and controller records. Check live enrollment, project launch rights, and GitHub access. “Grant only the manifest closure needed for a pull.” Bind scoped staging grants to an expiry. Fence locally revoked authority, and allow an eligible background publication to complete after the workspace operation.
- [X] T012 Integrate policy reads and shared storage activation in `internal/imagecache/built_policy.go`, `internal/imagecache/settings.go`, and `internal/app/image_cache_settings.go`. Initialize fresh MVP organizations with built caching On and the plan’s proposed 14 GiB shared limit under existing watermarks. Honor configured limits. Open usable shared storage when either cache class can retain content. Expose a recoverable unavailable state and allow ordinary launch if storage cannot be used.

**Checkpoint** — Tests establish storage bounds and current authority, and the runtime can load independent built-image policy.

## Phase 3: User Story 1 - Launch using a project’s built image (P1)

**Goal** — Fresh workspaces automatically use a matching project image, or build normally and publish in the background.

**Independent test** — Launch two fresh workspaces as authorized members of the same project. Verify the first builds and can publish while the second reaches a usable IDE with zero build or Feature installation. Verify another project using identical source cannot read its image. Confirm ordinary fallback for missing, evicted, or incomplete content.

### Tests first

- [X] T013 [P] [US1] Write commented negotiation and transport contract tests in `tests/contract/built_image_cache_test.go`. Cover exact-config matching across commits and branches, separate project and platform scope, revoked or stale grants, private object and mount authorization, interrupted staging, and the first complete eligible publication winning among overlapping misses.
- [X] T014 [P] [US1] Write commented guest tests in `internal/guest/built_image_cache_test.go` for a hit without a build, fallback on denial or invalid image before lifecycle execution, exact bytes supplied to the CLI, retained-home recreation, and a publication failure that leaves the IDE outcome intact.

### Implementation

- [X] T015 [P] [US1] Implement launch lookup and ordinary publication in `internal/app/built_image_cache.go`. Enforce “live policy and project access, matching repository, path and config bytes, and a complete manifest, config and layer closure for the requested platform.” Return an immutable authorized digest after acquiring that closure. Track the attempt through “admitted → receiving → complete → promoted, discarded, or expired.” Verify origin outside the write transaction, then recheck current project revision, project and GitHub authorization, policy generation, and dispatch at atomic promotion. Finish is idempotent. For matching concurrent launch misses, the first complete eligible attempt wins.
- [X] T016 [P] [US1] Package the pinned CLI integration proved in Phase 1 through `platform/packer/install-toolchain.sh`. Capture the immutable build output before UID adaptation and accept a verified local image at the resolver boundary. Preserve startup config parsing and runtime metadata and run existing Feature policy, initializer, and lifecycle behavior.
- [X] T017 [US1] Wire lookup, staging and finish negotiation and the proved single-platform OCI method sequence in `internal/web/guest_built_image_cache.go` and `internal/app/guest_server.go`. Enforce the exact private namespace, scoped authorization before object-existence checks, size and digest admission, bounded read/write deadlines, and cancellation. Keep public upstream and private routing separate.
- [X] T018 [US1] Implement pinned digest pull and scoped background push in `internal/guest/built_image_cache.go`, `internal/guest/image_cache.go`, and `internal/guest/agent_poll.go`. Use the explicit private host and existing guest CA and rotating certificate. Bound the upload worker and tag lifetime, support safe retry, and keep upload outside the wait for a usable IDE.
- [X] T019 [US1] Integrate lookup and image identity in `internal/guest/executor.go`, `internal/guest/devcontainer.go`, and `internal/guest/home.go`. Use the exact config bytes, pass a hit through retained-home recreation, and fall back through the normal network-policy build before lifecycle execution. Queue the immutable output of an eligible build and preserve checkout, mounts, runtime credentials, and IDE gating.

**Checkpoint** — Launch reuse works independently with an enabled test fixture.

## Phase 4: User Story 2 - Refresh using Rebuild container (P2)

**Goal** — The existing action refreshes the current project image when its actual build config matches the project’s current origin and ref.

**Independent test** — With a prior image present, rebuild a matching workspace and verify the old image serves until promotion. Rebuild from a stale config and interrupt a matching upload. Verify local rebuild behavior and preservation of the prior cache image.

### Tests first

- [X] T020 [P] [US2] Add commented controller tests in `internal/app/built_image_rebuild_test.go` for current origin and ref comparison, stale builds and origin changes during upload, interrupted and disk-pressured replacements, and a reader continuing to use its pinned old digest.
- [X] T021 [P] [US2] Add commented guest tests in `internal/guest/rebuild_test.go` for bypassing cache lookup, publishing the image the existing action produces, preserving dirty working files and retained mounts, and handling a publication decline independently of the local rebuild.

### Implementation

- [X] T022 [P] [US2] Extend explicit-rebuild attempt and promotion handling in `internal/app/built_image_cache.go`. Hold the previous matching closure during a bounded replacement attempt and decline replacement if both images cannot fit. Check live origin and local authority before replacing the pointer. A failed rebuild, interrupted upload, or mismatch leaves the prior eligible pointer in service.
- [X] T023 [P] [US2] Wire **Rebuild container** in `internal/guest/rebuild.go` and `internal/guest/executor.go` to run the usual local build with cache lookup bypassed and enqueue its actual immutable result for a scoped rebuild attempt. Preserve the existing mounts and local result.

**Checkpoint** — Rebuild publication is independently verifiable with a prepared slot and enabled policy.

## Phase 5: User Story 3 - Organization-wide control (P3)

**Goal** — Administrators can persist the independent built-image setting and disable reuse immediately while deletion runs in the background.

**Independent test** — As a member, attempt a settings change and observe denial. As an administrator, save Off during an upload, reload to verify saved state, and verify old reads and publication are fenced. Confirm background purge and successful new publication after re-enable.

### Tests first

- [X] T024 [P] [US3] Add commented settings API tests in `internal/web/image_cache_routes_test.go` for view and manage permissions, CSRF, enabled-boolean validation, version preconditions, audit, and save/read persistence.
- [X] T025 [P] [US3] Add commented policy and purge tests in `internal/app/built_image_policy_test.go` for immediate read and write fencing, active transfer cancellation, idempotent toggle, retry after restart, active and retired roots, shared public bytes, and disable followed by re-enable during cleanup.
- [X] T026 [P] [US3] Add commented UI tests in `ui/features/admin/registry-cache.test.tsx` and `ui/tests/e2e/registry-cache.spec.ts` for finding the setting, keyboard operation, disable consequences, permission states, save error and stale version, and save/reload.

### Implementation

- [X] T027 [US3] Extend `internal/web/image_cache_routes.go` and `internal/app/image_cache_settings.go` to return the built-image policy and effective availability and register the versioned immediate update. Commit enablement, generation and purge intent atomically with audit. Fence new admissions and cancel active grants on Off before returning success, independently of public storage activation.
- [X] T028 [US3] Reconcile purge in `internal/app/built_image_purge.go` and `internal/app/retention.go` across active and tracked retired roots. Remove disabled generations’ links and unleased exclusive bytes and preserve public and newer-generation references. “Clear a cleanup cutoff only after deletion succeeds or all eligible content is confirmed absent.” Reconcile and retry in the background after failures and restart.
- [X] T029 [US3] Document management response and update enforcement in `specs/001-shiftbox-mvp/contracts/openapi.yaml` and `specs/001-shiftbox-mvp/contracts/authorization-matrix.md`. Regenerate `ui/lib/api-types.ts` with `sh scripts/check-openapi.sh --write` and verify the route and type contracts in `tests/contract/openapi_routes_test.go` and `tests/contract/openapi_types_test.go`.
- [X] T030 [US3] Add the independent **Built devcontainer images** switch, persisted status, storage availability, and save handling to `ui/features/admin/registry-cache.tsx` and `ui/lib/use-image-cache.ts`. Consult Mantine MCP, reuse existing settings and Carbon/ShiftBox styling, and show the disable consequence before save with accessible feedback.

**Checkpoint** — Authorized administrators can operate the feature end to end and reload persisted state.

## Phase 6: Polish and cross-cutting verification

- [X] T031 Run the focused Go, migration, pinned-platform, and UI checks and static browser build in `specs/009-devcontainer-image-cache/quickstart.md`, including guest protocol and existing public pull-through regression cases. Verify the shipped controller assets and guest-toolchain pins using `tests/release/platform_image_test.go` and the existing embedded-asset check.
- [X] T032 Publish the required guest artifact and deploy the controller and UI to `shiftbox-demo` using `AGENTS.md`, `platform/packer/install-toolchain.sh`, and `platform/terraform/aws/README.md`. Verify the default AWS account and region, follow the approved demo schema procedure, review the Terraform plan and verify the running digest and task revision.
- [X] T033 Run the functional, authorization, recovery, and save/reload checks in `specs/009-devcontainer-image-cache/quickstart.md` on the demo. Record the review URL, revision, and results in `specs/009-devcontainer-image-cache/validation-report.md`. Clean up disposable resources and verify cleanup.

## Dependencies and execution order

```text
T001 + T002 → T003 → Phase 2 foundation
Phase 2 → US1 launch reuse
US1 → US2 rebuild publication
Phase 2 → US3 admin and purge
US1 + US2 + US3 → T031 → T032 → T033
```

- Phase 2 tests T004–T007 run first. T008 establishes durable state. T009 and T010 can run together, followed by T011 and T012.
- In US1, write T013 and T014 before T015 and T016. T017 uses T015. T018 uses the private transfer contract, and T019 integrates T016 and T018.
- In US2, complete T020 and T021 before the two implementation tasks. US2 reuses US1’s guest upload and controller publication paths.
- In US3, write T024–T026 first. T027 supplies settings writes and fencing. T028 implements durable cleanup. T029 and T030 consume the settings contract. Start US3 test work after Phase 2. Coordinate integration with US1 and US2 so edits to shared cache and listener files stay ordered.
- Run final verification and rollout once all three stories are complete. In particular, the admin backend and discoverable UI ship together.

## Parallel examples

| Story | Example batch |
| --- | --- |
| US1 | Run T013 and T014 together. Once both tests are written, run T015 on controller publication while T016 packages the guest CLI integration. |
| US2 | Run T020 and T021 together. After the tests, controller replacement T022 and guest integration T023 can run separately. |
| US3 | Run T024, T025, and T026 together in the API, runtime, and UI test files. |

## Implementation strategy

1. Prove the pinned guest and transfer seams, then complete the shared store and authorization.
2. Use US1 as the first internal MVP checkpoint. It proves that a fresh authorized launch can publish and that the next launch can reuse its image.
3. Add US2 and US3. The release scope includes rebuild publication and the administrator’s organization-wide control.
4. Deploy once all three stories pass their functional checks and the demo validation is ready to run.
