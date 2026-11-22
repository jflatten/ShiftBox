# Tasks: Embedded OCI pull-through image cache

**Input**: [spec](spec.md), [plan](plan.md), [research](research.md), [data model](data-model.md), [API](contracts/cache-api.md), [UI matrix](contracts/admin-ui.md), [validation](quickstart.md).\
**Branch/base**: `codex/004-oci-pull-through-cache` from saved `feature/mvp` at `798d5299d5798da664b68269e9963087a27a5137`; both HEADs and merge base match, zero commits difference. Preserve unrelated keep-alive changes.\
**Scope**: Runtime implementation authorized in the implementation worktree. Tests-first means write the small relevant test, observe it fail, implement and pass it; every test has a behavior comment. Use existing suites/fixtures and a few cohesive cache tests, not a test framework or exhaustive compatibility matrix. Check tasks off immediately upon actual completion.

## Phase 1 — Completed planning

- [x] T001 Read `AGENTS.md`, architecture references, constitution, current feature/implementation and relevant skills; inspect status, preserve unrelated work and verify the exact `feature/mvp` base.
- [x] T002 Complete `specs/004-oci-pull-through-cache/` spec, plan, research, data model, contracts, diagram, focused validation guide and ordered tasks; add the administrative UI completeness rule to tracked root `AGENTS.md`; deliver documentation to saved feature/mvp checkout.
- [x] T003 Run formal specify → plan → tasks → analyze, apply authorized documentation fixes and rerun analysis until no unresolved findings at any severity; save pass history and requirement/UI/test mapping in `specs/004-oci-pull-through-cache/analysis.md`; check links, dependencies, diffs, matching delivery copies and unrelated-file preservation. Planning only; this task reopens when user steering changes the documents.

## Phase 2 — Foundation and protected pull-through proof

**Goal**: Establish the selected library and unchanged-reference guest path safely. Initial proof is an internal checkpoint, not backend-only delivery.

- [x] T004 Recheck/pin safe compatible Distribution v3/API and actual dependency licenses in `go.mod`, `go.sum`, `third_party/distribution/patches/README.md`; record the narrow transport/tag/admission patch inventory and exact source in `research.md` (depends: T003).
- [x] T005 [P] Add focused commented failing tests in `internal/imagecache/cache_test.go`: instance-local outbound client guards registry/redirect/token/blob/DNS paths, private destinations denied, public anonymous challenge succeeds, initialization outage can use cached state, read-only methods and initial pre-write/no-retention guard. Reuse one HTTP/DNS/storage fixture (depends: T004).
- [x] T006 [P] Extend `internal/app/guest_server_test.go` and `internal/guest/agent_poll_test.go` with focused failing tests for current guest identity/revocation, transfer-aware timeout, managed hosts/certificate rotation, original refs, CLI metadata pre-pull and actual embedded Docker builder selection (depends: T004).
- [x] T007 Embed Distribution and narrow maintained patch in `internal/imagecache/{cache,upstream,storage}.go`, `third_party/distribution/patches/`, `internal/app/{controller_runtime,guest_server,image_cache}.go`: guarded per-instance clients, upstream-isolated repository links/shared digests, minimal all-write admission, read-only guest mTLS handler and lifecycle; disable competing TTL cleanup. No global transport/credentials or sidecar (depends: T005, T006).
- [x] T008 Configure original-reference pulls and embedded Docker BuildKit in `internal/guest/{image_cache,devcontainer,agent_poll}.go`, verifying current CA/client leaf, containerd store, `_default`/ns and CLI metadata-on-miss. Use existing Packer tooling; change `platform/packer/install-toolchain.sh` only if needed. Prove one protected public pull/build with bounded disposable storage and record it in `validation-report.md`; stop if transparency requires project edits or weaker policy (depends: T007).

## Phase 3 — US1: Reuse public image bytes transparently (P1)

**Independent check**: Fresh prebuilt and Dockerfile launches retain original references/files; a warmed cache reuses payloads by digest. One primary image fixture plus a second public-registry compatibility check is sufficient; no full product/version matrix.

- [x] T009 [US1] Extend the same `internal/imagecache/cache_test.go` fixture with a few failing cases: tags revalidate/digests reuse; two simultaneous pulls reserve before writes and protect active content; shared blobs count once; oversized/partial/interrupted content cannot overfill or become warm; cache-byte ratio excludes metadata and direct/guest-local reuse (depends: T008).
- [x] T010 [US1] Complete storage/tag/admission/LRU and restart cleanup in `internal/imagecache/{cache,storage}.go`: count temp/reserved/retired bytes, protect actual volume, evict unused unleased content before retention, preserve complete stale-image closures, and stream without cache writes when no room remains. Do not delete workspace/DB/key data or add new storage infrastructure by default (depends: T009).
- [ ] T011 [US1] Add bounded pull summaries and existing phase timing integration in `internal/imagecache/cache.go`, `internal/guest/devcontainer_timing.go` and workspace/operation reporting: controller-local/upstream bytes, metadata separate, partial/stream/direct/guest-local clearly labeled, unknown data not fabricated (depends: T010).
- [x] T012 [US1] Run the focused original-ref prebuilt/Dockerfile/second-registry check from `quickstart.md`; capture actual builder/cache requests, tag/digest and unchanged project hashes in `specs/004-oci-pull-through-cache/validation-report.md`. Use these fixtures again for final direct/cold/warm proof (depends: T011).

## Phase 4 — US2: Preserve network authority and truthful failure state (P1)

**Independent check**: Restricted guests use the controller image-only route, never new direct access; complete stale image has exact warning through Running; verification recovery does not change the running digest.

- [x] T013 [US2] Add focused failing outage/fallback tests to `internal/imagecache/cache_test.go`, `internal/guest/agent_poll_test.go` and existing workspace-read tests: complete cached outage succeeds, incomplete cache/denial does not, recovery leaves running digest unchanged, no-retention uses no temp writes, direct fallback obeys effective egress. Treat TLS/security errors and 429 as non-stale; honor Retry-After without an extra retry subsystem (depends: T012).
- [ ] T014 [US2] Finish classified tag fallback/verification, per-workspace freshness and policy-safe streaming/direct behavior in `internal/imagecache/cache.go`, `internal/guest/image_cache.go`, `internal/web/workspace_read.go` and next-version `migrations/{sqlite,postgres}/`; reuse existing guest revocation/operation machinery, never blindly rerun devcontainer hooks (depends: T013).
- [x] T015 [US2] Add a small failing workspace-warning test to existing `ui/features/workspaces/workspaces.test.tsx` and Stepper tests: exact persistent amber warning, Running state independent and successful verification versus actual running image distinct (depends: T014).
- [ ] T016 [US2] Wire freshness into `ui/features/workspaces/{workspace-detail,launch-flow}.tsx` and existing workflow Stepper/state Chips; run the focused outage/recovery/restricted-egress journey in `quickstart.md` and record UI-12 evidence in `validation-report.md` (depends: T015).

## Phase 5 — US3: Complete administrative UI and safe settings (P1)

**Independent check**: One admin save/reload/pressure/path-change journey plus one permission-denial journey covers UI-01–UI-13; mock tests do not establish live persistence. No new purge, credentials, allowlist or push controls.

- [x] T017 [P] [US3] Add focused failing settings/API tests in `internal/web/image_cache_routes_test.go` using existing database/authorization fixtures: enabled requires maxBytes “positive whole bytes within signed 64-bit range”; “0 < volumeLowPercent < volumeHighPercent < 100”, default high/low 80/70; safe absolute path, invalid path preserves active config, valid move drains/starts empty/retains old files, version/CSRF and V/M/member checks; include one restart/migration check (depends: T016).
- [x] T018 [P] [US3] Add focused failing component tests in `ui/features/admin/registry-cache.test.tsx` and draft `ui/tests/e2e/registry-cache.spec.ts` journeys for explicit save/reload, proposed URL-tab persistence, desired/effective/failed state, usage and byte labels, loading/empty/error/retry, keyboard access, cached-image completeness, query authorization and nonadmin denial. Cover the matrix through these journeys, not one suite per row (depends: T016).
- [x] T019 [US3] Implement settings/activation and dual-dialect next-version migrations in `internal/imagecache/settings.go`, `internal/app/image_cache.go`, `migrations/{sqlite,postgres}/`; expose GET/PUT in `internal/web/{image_cache_routes,routes}.go`/`internal/app/management.go`; update canonical `specs/001-shiftbox-mvp/contracts/openapi.yaml` and generate `ui/lib/api-types.ts`. Include a bounded read-only cached-image inventory in the same GET. Stage/drain/activate with existing operations; preserve effective settings on failure, avoid filesystem waits in SQL transactions (depends: T017, T018).
- [ ] T020 [US3] Build **Cache** tab with settings and read-only cached-image inventory in `ui/features/admin/{infrastructure,registry-cache}.tsx` and scoped `ui/lib/use-image-cache.ts` per all matrix rows; consult Mantine MCP, keep Carbon/branding, Stepper/chips, validation and permissions. Run the two real-backend E2E journeys, including pressure/streaming and path save/reload, and record results in `validation-report.md` (depends: T019).

## Phase 6 — Finish and review the feature

- [x] T021 Add only focused missing packaging/compatibility checks using existing `tests/release/`, `internal/backup/` and `tests/contract/guest_protocol_test.go`: actual linked license notices, settings included/payload excluded, empty-cache restart/restore and N/N−1 guest behavior. Reuse existing migration/recovery tests; do not build a new test framework (depends: T020).
- [x] T022 Finish third-party licenses/notices and narrow patch documentation in `third_party/` and `platform/container/Dockerfile`; integrate backup/cache exclusions and safe upgrade/rollback in `internal/backup/`, guest release metadata and `quickstart.md`. Run relevant Go/race/contracts/UI/typecheck/build/embedded-asset checks once; record outcomes and remaining limits in `validation-report.md` (depends: T021).
- [x] T025 Let administrators publish validated EC2 releases to a named AMI family and select that family in resource profiles. Use the newest compatible available member at new workspace admission, preserve exact workspace bindings and existing pinned profiles, and verify the administration workflow and patch rollover (depends: T022).
- [x] T023 Follow complete root `AGENTS.md` backup/migration/build/Terraform rollout to shiftbox-demo, default profile/account 524941137496/us-east-2; review intended plan, confirm ECS stability/digest and live UI. Run the small direct/cold/warm comparison from `quickstart.md` using fresh VMs, same architecture/digest and no preload/guest-cache confounder; record actual bytes/IDE timing, review URL and ECS revision in `validation-report.md`. No fabricated speed target (depends: T025).
- [x] T026 Bind-mount the bundled code-server read-only in new and rebuilt development containers. Build the named platform AMI and record a warm-first and cold-second m6i.xlarge comparison against the original copy-based launch (depends: T023).
- [x] T027 Shorten fresh workspace launches with a literal-image pull, the project's effective user ID policy, a lightweight IDE socket probe, and 300 MiB/s EC2 root initialization. Verify one combined warm-cache launch on the demo (depends: T026).
- [x] T028 Count verified embedded OCI image config bytes in cached-image inventory and confirm the Python attestation displays Complete on `shiftbox-demo`.
- [x] T029 List retained public and published project devcontainer images in the protected cache API. Show date added, retained layer sizes in MB, and 20 images per page in the admin table. Verify the list in the demo.
- [x] T030 Show total retained layer size in MB for each cached image, with layer details collapsed. Verify on `shiftbox-demo`.
- [ ] T024 Clean only disposable test workspaces/profiles/storage/tags/cloud resources; preserve existing resources, operator backups and local state. In `specs/004-oci-pull-through-cache/validation-report.md` link G1–G7 and every matrix row to the few actual checks, report limitations and sign off only when UI plus eviction and all accepted behavior work (depends: T023).

## Dependencies and parallel work

```text
T001–T003 → T004 → (T005 || T006) → T007 → T008
  → T009 → T010 → T011 → T012 [US1]
  → T013 → T014 → T015 → T016 [US2]
  → (T017 || T018) → T019 → T020 [US3]
  → T021 → T022 → T025 → T023 → T024
```

[P] tasks run in parallel inside separate subagents after their shared dependency: T005/T006 and T017/T018 touch different files. Respect five maximum by project rule or the smaller available session limit. Migrations, canonical contracts and shared app wiring remain sequential.

**Totals**: 27 tasks: planning 3, foundation 5, US1 4, US2 4, US3 4, release 7. Four [P] tasks in two groups. The completed feature requires all stories, full admin UI and production disk safety; the initial protected proof is not a shippable backend-only increment. Do not enlarge the test suite to clear analysis findings: narrow/defer unrequested behavior while preserving the agreed acceptance gates.
