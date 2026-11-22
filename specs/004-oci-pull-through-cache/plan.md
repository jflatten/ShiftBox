# Implementation Plan: Embedded OCI pull-through image cache

**Branch**: `codex/004-oci-pull-through-cache` | **Date**: 2026-11-17 | **Spec**: [spec.md](spec.md)

## Summary

Add a read-only public OCI pull-through cache to the existing controller process using a narrowly patched, pinned CNCF Distribution v3 library. Configure ShiftBox-managed Docker/containerd and its embedded BuildKit on guests to retain original image references. Deliver the administrative UI, safe production eviction, workspace freshness and real validation as one feature. The first pull-through proof is an internal checkpoint, never a backend-only release.

Current evidence establishes source paths, not runtime support or speed. [Research](research.md) records official sources and [quickstart](quickstart.md) defines execution gates. No runtime implementation, proof tests or deployment were performed during planning.

## Technical Context

| Area | Concrete approach |
| --- | --- |
| Languages/versions | Repository declares Go 1.27.4; static UI Next 16.4.6/React 19.3.1/TypeScript 5.9.3/Mantine 9.8.0/TanStack Query 5.107.1. These are inspected manifest values, not a fresh package upgrade recommendation. |
| Dependency | Distribution v3.1.2 is the official latest release observed during planning. Recheck official advisories/API before pinning and maintain a small module patch set; do not silently mutate global transports. |
| Guest tools | Packer pins Docker 29.9.3, containerd 2.4.2, Buildx 0.39.0, Dev Container CLI 0.90.0. Prefer the Docker driver/embedded BuildKit, whose source receives Docker RegistryHosts; prove both pull and FROM paths. |
| Storage | Extend existing installation settings and operation/workspace summaries in SQLite/PostgreSQL; Distribution filesystem content with shared digests and upstream-isolated links. No extra DB/service or telemetry store. |
| Test tools | Existing Go tests/httptest, race checks on coordinator, Jest/Testing Library, Playwright against the real Go TLS controller, contract checks and bounded release scenarios. Unit tests first, comments on every test. |
| Platform/scope | One controller, managed VM guests; both existing UTM and EC2 boundaries retained. Shared AWS testbed is shiftbox-demo only. This is functional proof, not deferred scale/load testing. |
| Performance aim | Reduce repeat upstream image bytes and measure launch-to-IDE; extraction, Dockerfile RUN, hooks and configuration costs remain. No established timing improvement. |
| Engineering gates | Arbitrary-registry `_default`/`ns` routing, mTLS rotation, actual builder path, no-retention streaming and complete outbound transport coverage must pass before enabling support. Failure blocks implementation release; it does not reopen library selection. |

## Constitution Check

| Principle | Before research | After design |
| --- | --- | --- |
| KISS | Reuse controller/Distribution and guest runtime | Pass as planned: no sidecar, generalized artifact platform, admin allowlist or push cache. Narrow patch is necessary because stock internals lack transport/admission hooks. |
| Explicit trust | Untrusted VM and controller-owned policy retained | Pass as planned: per-VM current authorization, public-destination transport, image-only restricted exception; no global keychain/transport. |
| Work preservation | Cache disposable; workspace files untouchable | Pass as planned: managed-content-only eviction; preserve DB/keys/old cache paths and active settings on failure. |
| Test first | Commented real failure tests required | Pass in plan; runtime evidence outstanding. No code was written in planning. |
| Visible workflows | Administrative and workspace coverage mandatory | Pass in plan: matrix, Mantine MCP selection, Stepper/chips, permissions and E2E required. |

The immutable reference architecture predates approved EC2 work and this cache decision. Existing `specs/002-aws-ec2-provider/` records the deployed topology; the present user-confirmed cache scope supplies the explicit image-only egress exception. No reference or constitution amendment is made. All implementation and support claims remain gated.

## Implementation Sequence

1. **Compatibility and protected pull-through proof.** Recheck/pin Distribution and patch its transport/construction/tag-error hooks; cover public-destination enforcement in one shared fixture before any real request. Wire guest-only reads with existing identity. Prove original-reference pulls and actual builder FROM routing on bounded disposable storage with logs. A minimal reservation/no-retention boundary must exist before this proof; do not test an unbounded stock proxy on the installation volume.
2. **Production content and disk safety.** Complete shared CAS namespaces, digest verification, complete-image closure, all-write admission, eviction, lease/cancellation/restart handling and physical-volume pressure protection. The cache may retain only admitted bytes; no-retention streaming remains read-only against cache disk.
3. **Failure truth and guest integration.** Finish freshness/outage classification, policy-constrained fallback and guest lifecycle configuration; emit operation-associated observations and durable workspace freshness. Never rerun completed devcontainer hooks just to retry a pull.
4. **Settings and administration.** Migrate authoritative settings, implement staged activation and API, then the discoverable **Cache** tab with configuration and read-only image inventory, all matrix rows and workspace warnings. Tests precede each layer. Wire read-only and manage permissions, state recovery and save/reload checks.
5. **Packaging and release proof.** Complete linked-dependency license/notice inventory, guest/release compatibility, backup exclusions, upgrade/rollback, UI runtime proof and direct/cold/warm measurements. Deploy only during separately authorized implementation using AGENTS.md, then clean disposable resources.

## Project Structure and File Targets

Paths below are proposed unless described as existing. Select the next available migration number at execution; do not reserve today's latest schema number or edit existing migrations.

| Target | Work |
| --- | --- |
| `internal/imagecache/{cache,upstream,storage,settings}.go`, `cache_test.go` and existing integration fixtures | Small cache boundary; context-bound clients, normalized origins, library adapter, global leases/accounting, SQL configuration and pull outcomes. Keep helpers together until a real readability need justifies splitting; no per-edge-case files or framework. |
| `third_party/distribution/patches/`, `go.mod`, `go.sum` | Reproducible pinned module adaptation with upstream base, patch rationale, license and modified-file notices. `registry/proxy` and internal client surfaces are listed in research. Keep its module identity so internal imports work. |
| `internal/app/{controller_runtime,management,guest_server}.go`, new `image_cache.go` with existing guest-server tests | Compose one service and lifecycle in existing process; guest-only OCI route and transfer-aware deadlines; health and configuration activation. |
| `internal/web/{routes,route_actions,infrastructure_routes,workspace_read}.go`, new `image_cache_routes.go` with focused tests in the same API fixture | Admin API, validation/version conflicts/CSRF/audit, safe runtime handoff after authorization, workspace freshness views. No filesystem/network waits inside SQL write transactions. |
| `internal/guest/{devcontainer,devcontainer_timing,protocol,agent_poll}.go`, new `image_cache.go` with existing guest tests | Stage root-owned hosts configuration, original-reference pre-pull when needed, select/verify embedded Docker builder, identity rotation and policy-safe fallback. |
| `internal/workspace/`, `internal/operation/`, `internal/backup/`, `internal/config/`, `migrations/{sqlite,postgres}/` | Current operation association, bounded freshness persistence, settings activation/backup semantics and dialect-equivalent migration. Preserve unrelated maintenance work. |
| `platform/packer/install-toolchain.sh`, platform image manifest/verification, `platform/container/Dockerfile`, `platform/terraform/aws/` | Verify containerd image store and builder support on admitted platform; release inventory/notices. Terraform only if cache port/mount/storage guard requires infrastructure changes. |
| `ui/features/admin/{infrastructure,registry-cache}.tsx`, `ui/lib/{use-image-cache,api-types}.ts` | Add the **Cache** tab to existing `/infrastructure/`; scoped queries, explicit save, effective state and a read-only image list derived from retained manifests and repository links. |
| `ui/features/workspaces/{workspace-detail,launch-flow}.tsx`, `ui/components/workflow-stepper.tsx` | Persistent amber freshness warning and accurate running digest; real-time image preparation status in existing Stepper. |
| `specs/001-shiftbox-mvp/contracts/openapi.yaml`, `internal/web/image_cache_routes_test.go`, `ui/tests/e2e/registry-cache.spec.ts`, existing `tests/release/` fixtures | Existing canonical OpenAPI updated only in implementation; generated TS, real backend/browser cases and release evidence. This feature's proposed contract is separate today. |

## Controller and Transport Design

`handlers.NewApp(ctx, config)` provides the embedded handler and `Shutdown()`. A pinned module patch introduces per-instance options for outbound clients, classified tag outcomes and storage admission. Initialization must work from cached state during an upstream outage and return controlled failures to the adapter. Audit every constructor/ping/token/redirect path; stock proxy uses hidden default clients and accepts too-broad stale fallback. Never assign to `http.DefaultTransport`, default clients, process metrics or a controller-global credential provider to make embedding work.

A bounded lazy dispatcher keys proxy instances by normalized HTTPS upstream authority (including eligible port). Validate incoming OCI routes and `ns`; do not accept arbitrary URL forwarding. Map repository metadata through a storage-driver adapter: namespace repository/link paths by an encoded canonical upstream, keep blob digest paths shared. Preserve the original remote repository name. Ensure canonicalization cannot collide or escape paths. Bound instance count and active concurrency; release idle app resources without deleting persisted content. One application-level lifecycle owns metrics, shutdown and eviction; disable stock TTL/upload purging so it cannot race ShiftBox leases.

Public destinations only: validate every URI and DNS answer, reject non-global/private/local/link-local/multicast/reserved addresses, pin approved resolved IP to each dial while preserving TLS hostname verification, revalidate reconnects and redirects. No environment proxy bypass, userinfo, cleartext downgrade, controller headers/cookies/keys or inherited registry credentials. Strip sensitive headers across origins; anonymous token service exchanges use only public pull scopes and never log bearer tokens. Treat foreign-layer URLs as upstream fetches under the same policy. Bounds cover DNS/connect/headers/idle body, redirects, response metadata and cancellation. Public registries whose protocol/TLS is invalid fail visibly; eligibility is not a promise that every registry implementation is compatible without proof.

## Keep Implementation and Verification Small

Use existing context deadlines and fixed-size streaming buffers, a small worker/handler pool and one admission coordinator. Do not introduce new operator tuning controls or arbitrary numerical handler/timeout/buffer defaults in this feature plan. Concrete internal constants follow the supported libraries and focused proof; overload cancels/fails visibly instead of allocating unbounded work. Disk reservations and current physical free space are always checked before writes regardless of pool size. Reuse existing operation/history retention and polling for summaries/freshness recovery; do not add a retry or telemetry subsystem.

The focused validation guide has six combined checks. Extend existing fixtures and a few cohesive suites; do not make every failure variant its own file/task or multiply registries × platforms × builder versions. Unsupported OCI extensions and broader interoperability work are deferred. Required isolation at registry, redirect, token, blob and DNS paths remains covered by representative cases in one fixture.

## Guest Routing and Identity

Use existing guest HTTPS listener/certificates, current enrollment authentication and connection revocation registry. Prefer an OCI route `/v2/` on the authenticated poll listener with route-specific transfer deadlines via the HTTP response controller, keeping control RPC bounds intact. Current `listenGuest` imposes 20-second read/write timeouts; large transfers require explicit override, idle progress bounds and revocation/leaf-expiry cancellation. If this cannot be safely isolated on the existing listener, a dedicated internal listener in the **same process** is a gate requiring corresponding Terraform network settings and admin effective endpoint display; no extra service.

Configure root-owned `/etc/docker/certs.d/_default/hosts.toml` and any required registry-specific override from desired guest settings, using installation CA and the current per-VM client leaf/key. Rotation atomically replaces files and verifies new connections; never use browser or enrollment credentials. Configure restricted mode with `server = cache` (no automatic parent). Validate that the pinned resolver supplies the original `ns` even in server mode; preserve original hosts/references and TLS identity. Unknown or revoked guest identity must fail on every new request and active streams close on revocation/expiry.

Keep the Buildx **docker driver** backed by embedded BuildKit, if proof confirms it uses the shared host configuration. Record the actual driver from build logs; generic BuildKit TOML is not proof of this path. Configuration is installed before image preparation, without disrupting a running devcontainer. Use CLI-resolved JSONC configuration rather than a hand-written parser to pre-pull original prebuilt references if CLI metadata lookup would otherwise escape the cache. Prove Features/metadata do not accidentally bypass network policy; extending this into a general artifact/Feature cache is not in scope.

Prefer controller no-retention streaming for capacity refusal. If the controller is unavailable and workspace egress permits, guest configuration can select the original server as parent; restricted profiles retain mirror-only routing. Before accepting automatic resolver fallback, prove error classification and endpoint selection against the actual builder. If hosts configuration cannot limit fallback correctly, keep automatic fallback disabled and implement a targeted guest image-preparation retry based on structured cache failure, before lifecycle hooks; never blindly replay `devcontainer up`. Unsupported routing/fallback is a technical release blocker, not permission for reference rewriting, TLS interception or weaker egress.

## Disk Admission and Eviction

One coordinator owns **all** blob, manifest, link, journal and temporary writes across upstream handlers. A managed-root inventory counts each physical shared digest once, includes retired roots and incomplete files, and reconstructs safely at restart. Retired files left after path moves continue consuming total cache budget and physical-volume usage until the operator reclaims them. Do not count a blob once per repository or image.

Before retaining a new object, reserve known size plus bounded metadata/temp overhead; evict least recently pulled/used unleased objects and links first. Reserve the selected platform image closure while serving it so eviction cannot invalidate a stale response mid-pull. Same-digest readers share one admitted transfer; canceling one reader must not corrupt another. Never persist a tag to a partially committed manifest or trust an incomplete blob as warm.

Both constraints apply: retained + temporary + concurrent reserved bytes must fit `maxBytes`; physical used bytes plus reservations must stay below the configured high watermark with filesystem-aware safety headroom. On pressure, evict toward low watermark and sufficient admission room, stopping if nothing eligible remains. Unknown lengths use bounded rolling reservations **before each write**, enforcing a hard upper bound; oversized objects stream without retention. Recheck statfs available space as writes progress; disk errors/other-process growth cancel retention and remove incomplete files. A small fixed bounded transfer buffer may exist in memory, never an unaccounted temp file. Use the existing filesystem space check plus the guarded write/reservation boundary; do not add a dedicated volume/quota subsystem unless the focused proof demonstrates it is necessary. These checks cannot guarantee other processes leave space. Report that limit truthfully. No database, key, backup or workspace data deletion.

Serve unretained upstream content through the same authenticated, public-only image path; verify digest streaming and never publish success for incomplete data. If digest verification fails after bytes have started, terminate response and report failure. Admission precedes Distribution `Create`/`Put`, including proxy metadata paths. A storage driver write guard is the final backstop; do not rely on evicting after completion.

## Freshness, Effectiveness and Operations

Classify DNS/connect timeout, connection failure and upstream 5xx as availability errors; 429 is a separate rate-limited failure: honor Retry-After within the operation deadline, then report it without stale fallback. 401/403/404, TLS validation, blocked destinations and invalid content fail closed. The complete cached selected-platform closure is required; no partial stale image. Background bounded verification of affected original references clears the availability warning only after successful upstream content verification. Denial/not-found/security outcomes remain visible failures and do not authorize a new stale pull; never report successful-current verification on denial. Never update running digest except from actual guest/container evidence. A changed upstream tag becomes `updateAvailable`, not “new version running.”

Attribute pulls to controller-authenticated guest/workspace and current durable operation; guest reports are hints, not permission. Aggregate per-original-reference byte observations, including builder bases, and persist a bounded summary for reload. Separate controller cached bytes, upstream image payload bytes, metadata/token bytes, no-retention bytes, direct guest bytes and guest-local reuse. See [contracts](contracts/cache-api.md) for exact denominators and missing-data handling. Do not infer IDE readiness from pull completion; retain existing phase timings and actual proxied IDE proof.

## Configuration, Migration, Restart and Rollback

Proposed default is disabled until an administrator saves a positive budget; default path `<installationDir>/cache/registry`, high 80%, low 70%. No invented unlimited budget. Settings include enabled, path, bytes, high/low and version. [Data model](data-model.md) defines validation. All operator-facing state/actions have [UI coverage](contracts/admin-ui.md).

Save validates candidate path/permissions/volume and limits without disturbing active configuration. Persist desired generation and activation state, stop new retained pulls, drain current transfers, activate the candidate then mark effective generation. New paths begin empty; old files remain inventoried and shown for manual reclamation. On failure retain effective settings and show reason; Save can retry the candidate. No automatic data move or admin deletion action is introduced. Budget reduction drains leases, evicts before activation and fails visibly if it cannot satisfy the new bound. Disabled mode drains then bypasses retention with explicit policy-safe behavior; restricted guests may still use the image-only no-retention path while the controller endpoint is available. “Disabled” means retention disabled, stated next to the control.

Migrations extend existing settings/workspace records only where required, using the next available schema version in both dialects, with existing installations disabled and no workspace mutation. Existing guests keep N/N−1 compatibility; do not silently restart dockerd on active workloads. Report unsupported guest capability in workspace/admin state and require the existing safe update/start path before cache use. Restart reconciles pending activation and incomplete writes against the last durable effective generation; service unavailability does not corrupt authority or block unrelated management.

Back up authoritative settings/activation/freshness, exclude cache payload and local rebuildable index; restore handles absent root as empty after validation and existing authority recovery. Before migration follow approved verified management backup and retained-guest release gates. A binary rollback alone does not undo schema or newer guest tooling. Roll back only against compatible schema/guests, disable retention and retain original image references; otherwise use approved coordinated restore/recovery. Preserve previous Terraform variables/digest for a controller-only rollback as AGENTS.md describes.

## Complexity Tracking

No constitutional violation proposed. The maintained Distribution patch serves required SSRF/freshness/disk guarantees; middleware alone cannot reach hidden client/storage paths. Shared CAS namespace adaptation serves required arbitrary registries and count-once storage. A single admission coordinator serves the hard disk boundary. No new generic registry management platform is proposed.

## Review and Release Gates

- G1: Pinned library patch/API/security and licenses; source-proven versus runtime evidence separated.
- G2: Original-reference prebuilt and actual Dockerfile builder proof on fresh guests across public registries.
- G3: Complete outbound isolation, current guest authority and restricted image-only path.
- G4: All-write disk admission, LRU/leases/restart and bounded no-retention streaming.
- G5: Exact stale warning, classification and verification recovery with truthful running digest.
- G6: Every admin matrix row and workspace state passes tests, save/reload, permissions and real runtime checks.
- G7: Packaging, compatible migration/rollback, direct/cold/warm evidence, demo deployment and cleanup.

All gates are **outstanding runtime work**. See [tasks.md](tasks.md) for ordered executable work and [checklist](checklists/requirements.md) for planning coherence.
