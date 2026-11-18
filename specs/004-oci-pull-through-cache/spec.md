# Feature Specification: Embedded OCI pull-through image cache

**Feature Branch**: `codex/004-oci-pull-through-cache`\
**Created**: 2026-11-17\
**Status**: Implementation-ready planning baseline; runtime validation outstanding.\
**Input**: User-confirmed public image caching decisions carried forward in the planning request. This document proposes engineering details within those decisions; it does not authorize runtime implementation or amend the reference architecture.

## User Scenarios & Testing

### User Story 1 — Launch with original image references (Priority: P1)

A developer launches a fresh workspace using a public prebuilt devcontainer or public Dockerfile base images. ShiftBox configures the VM automatically and reuses public image bytes between VMs.

**Why this priority**: Transparent reuse is the purpose of the feature.\
**Independent Test**: Launch two fresh VMs from an unchanged repository, compare image digests, upstream byte counts and launch-to-IDE timing; repeat for a Dockerfile build.

1. **Given** an anonymously accessible public image from MCR or another Internet registry, **when** launched cold then warm on fresh VMs, **then** the original image reference and repository files remain unchanged and retained layers are reused by digest.
2. **Given** a tag has changed, **when** requested again, **then** upstream is checked and the current manifest is resolved; unchanged layer digests are reused.
3. **Given** a fully cached digest, **when** requested, **then** immutable cached content can be served without a tag refresh.
4. **Given** a Dockerfile with public `FROM` images, **when** Dev Container CLI builds it, **then** the actual selected builder uses the cache without Dockerfile edits, using the existing supported guest platform and builder; no extended Dockerfile compatibility matrix is added.

### User Story 2 — Preserve network authority and explain failures (Priority: P1)

A developer can use allowed public image content under a restricted network policy, and sees a truthful warning if an upstream outage requires the previous cached image.

**Why this priority**: A cache must not become an unrestricted proxy or conceal freshness failures.\
**Independent Test**: Restricted-guest image-only pulls succeed through the controller; unrelated requests, revoked guests and private destinations fail. With a fully cached image, interrupt upstream availability and inspect startup and running workspace details.

1. **Given** an active enrolled guest, **when** it makes an OCI read, **then** current per-VM authority is checked; unknown, expired or revoked identity is denied and active revoked transfers are closed.
2. **Given** upstream availability fails and the previously resolved image's manifest/config/layers for the selected platform are all cached, **when** startup uses that image, **then** an amber warning remains visible even if workspace state is Running: **“The upstream registry could not be contacted. Using cached version.”**
3. **Given** verification later succeeds, **when** status refreshes, **then** the availability warning clears, while the running digest stays accurate and any newer upstream digest is identified separately. Recovery does not replace an already-running container.
4. **Given** denial, missing image, invalid TLS, malicious redirect/token realm or digest corruption, **when** pulled, **then** the failure is reported; stale fallback does not hide it.
5. **Given** cache failure or insufficient retention capacity, **when** upstream is healthy, **then** bounded no-retention controller streaming remains available where possible; direct guest fallback requires existing workspace egress permission. Restricted guests never gain direct Internet access.

### User Story 3 — Protect storage and operate through the UI (Priority: P1)

An administrator discovers Registry cache under Administration → Infrastructure, configures it, sees active settings and runtime effectiveness, and can diagnose errors without editing the database.

**Why this priority**: Backend-only delivery is explicitly unacceptable; disk safety is required before shipping.\
**Independent Test**: Open **Administration → Infrastructure → Cache**, save/reload settings, inspect active/effective generations and the read-only cached-image list, move storage during a pull, induce disk pressure and restart. Verify all rows of the [UI coverage matrix](contracts/admin-ui.md).

1. **Given** positive size budget and a writable safe path, **when** settings are saved, **then** server validation and permissions apply, settings survive reload/restart, and desired versus effective state is visible.
2. **Given** a new path fails validation, **when** saved, **then** the active path stays in use and the form explains the failure. A successful path change drains active pulls, begins empty at the new location and leaves old files for explicit operator reclamation.
3. **Given** volume pressure or concurrent incoming blobs, **when** admission runs, **then** least recently used unleased content is evicted before downloads write; cache and physical-volume bounds cover temporary and concurrent bytes. Active transfers are protected and shared content counts once.
4. **Given** a nonadmin or view-only administrator, **when** visiting or calling cache settings, **then** UI and backend enforce corresponding view/manage permissions. Loading, empty, failed-save, retry, dirty form and conflicting-write states are usable by keyboard.

### Required Failure Boundaries

Cover representative failures in the same small fixtures: blocked private registry/redirect/token/blob/DNS destination; unknown/revoked guest; upstream outage versus denial/incomplete cached image; two concurrent pulls under disk pressure; interrupted transfer; invalid storage path. These establish the agreed trust, disk and UI behavior without an exhaustive protocol/storage permutation matrix.

## Requirements

### Functional Requirements

- **FR-001**: Embed CNCF Distribution v3 as a library HTTP handler in the existing Go controller process and binary. No cache sidecar/service. Keep ShiftBox MIT and preserve third-party obligations.
- **FR-002**: Support anonymous public Internet OCI registries without an upstream allowlist or registry credentials. Anonymous bearer challenges are allowed. Reject private/local/link-local/reserved destinations on all controller outbound paths, including redirects, DNS and token/blob endpoints.
- **FR-003**: Preserve repository `devcontainer.json`, Dockerfiles and original references. ShiftBox owns guest pull and actual Dockerfile builder routing. Public prebuilt images and public Dockerfile bases are both required. Private images, credentials, pushes and sharing locally built results are deferred.
- **FR-004**: Namespace upstream repositories without collision; share immutable content by digest. Revalidate tag requests; digest reuse and metadata checks must not be mislabeled cold downloads.
- **FR-005**: Serve the complete previous cached image only for classified availability failures, with the exact persistent warning and recovery semantics in US2. Preserve running/resolved/last-verified digests separately.
- **FR-006**: Use current enrolled per-VM identity on guest-only HTTPS with transfer-appropriate bounds, rotation, expiry and active revocation. The controller fetch path is an explicit **public OCI image-read-only exception** for restricted guests, superseding the earlier MCR-only research, not general web forwarding.
- **FR-007**: Enforce a configurable storage location and hard total cache size limit; protect the underlying volume. Evict before writes/download retention, reserve concurrent/temp space, count shared content once, and never evict active transfers or user/database files. Proposed configurable high watermark: 80%; accepted low target: 70%.
- **FR-008**: Prefer bounded no-retention controller streaming when retention cannot be admitted. It must not create cache temp files or exceed the budget. Direct fallback may occur only where effective workspace egress already permits; policy is never weakened automatically.
- **FR-009**: Ship discoverable administrative UI for every operator setting/action/status, with durable settings, effective-state reporting, validation, permissions and end-to-end coverage as specified in the matrix. Use Mantine controls/Stepper/chips and Carbon/ShiftBox branding.
- **FR-010**: Show cache/volume usage, pressure, health, evictions, no-retention streaming, direct fallback and image-byte effectiveness. Distinguish guest-local reuse and partly cached pulls. Report measurements rather than promise a speedup.
- **FR-011**: Handle configuration activation, storage move, restart, upgrade and rollback safely; preserve active settings if validation fails. Cache data is disposable and excluded from management backup; authoritative settings and freshness records follow existing backup/restore protections.
- **FR-011a**: The **Cache** tab contains cache settings and a bounded read-only inventory of retained image references. Show source/reference, digest and recorded platform where known, and distinguish complete images from partial content without attributing shared bytes to individual images. Use the existing infrastructure read permission.
- **FR-012**: Write commented unit/contract/UI tests before implementation, then prove real runtime behavior on `shiftbox-demo` using the documented validation guide. All operator UI and production eviction are release requirements; an early pull-through proof alone is not delivery.

### Key Entities

Cache settings and activation; upstream identity and repository/tag resolution; global digest content and leases; pull observations; workspace image freshness. See [data-model.md](data-model.md).

## Success Criteria

- **SC-001**: Both prebuilt and Dockerfile journeys pass with unchanged repository hashes and original references on fresh VMs across MCR and at least one other public registry.
- **SC-002**: A fully warm retained-image pull reads all required image payload bytes from controller storage, with upstream metadata/token bytes measured separately. Direct, cold and warm measurements include actual launch-to-browser-IDE readiness; no numerical speed target is invented.
- **SC-003**: Every negative network/identity case in the validation guide is denied without restricted-guest direct fallback; stale content is served only in the allowed availability cases.
- **SC-004**: Under bounded functional concurrency/disk-pressure tests, cache-owned bytes plus reservations never exceed budget; volume pressure stops writes before the protected bound is crossed by ShiftBox. External writers are detected and trigger admission shutdown; no claim is made that ShiftBox can stop unrelated processes filling shared storage.
- **SC-005**: Every coverage-matrix row passes live UI/API/permission/persistence checks, including exact warning recovery while Running and failed storage activation. No implementation task is complete on backend evidence alone.

## Assumptions and Scope

One active controller. Reuse existing guest trust, operation events, SQL migration conventions, permission actions and static browser export. Engineering choices are detailed in [plan.md](plan.md); technical integration gates remain unproved, rather than unresolved product choices. No architecture reference document is changed. This planning pass runs no runtime tests or AWS operations.

## Delivery Simplicity

Use the six focused combined checks in quickstart.md and existing test infrastructure. Avoid a large new test suite, arbitrary tuning limits, extra storage infrastructure or speculative OCI protocol extensions. A single primary fixture plus a second public-registry compatibility check is sufficient. This bounds testing effort without dropping transparent routing, outbound/guest isolation, pre-write disk protection, stale recovery or complete administrator UI.
