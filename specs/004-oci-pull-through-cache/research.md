# Research: embedded OCI image cache

**Date**: 2026-11-17. Official source review and repository inspection only; no compilation, upstream pull proof, AWS test or deployment. Decisions below implement the settled product scope, rather than reopening product selection.

## R1 — Embed Distribution v3 with a maintained narrow adaptation

**Decision**: Baseline official latest observed release **v3.1.2**, then recheck official releases/advisories and pin the exact safe compatible module revision during T004. Retain its module identity with a reproducible source patch, avoiding invalid imports of Distribution `internal` packages from copied first-party code.

**Rationale**: [`handlers.NewApp(ctx, *configuration.Configuration) *App`](https://github.com/distribution/distribution/blob/v3.1.2/registry/handlers/app.go) provides ServeHTTP and Shutdown in process. The [release](https://github.com/distribution/distribution/releases/tag/v3.1.2) includes security fixes; [go.mod](https://github.com/distribution/distribution/blob/v3.1.2/go.mod) requires Go 1.26.0, versus the repo's Go 1.27.4. Source compatibility is plausible, unbuilt. Constructor panics and network initialization must become controlled adapter failures; cached-only startup must survive upstream unavailability.

**Alternatives considered**: External registry/sidecar and different cache libraries were already rejected by the user. Unmodified middleware-only embedding cannot enforce the required hidden outbound and write paths. No renewed alternatives selection is required.

| Upstream file/API | Verified gap | Narrow planned adaptation and test |
| --- | --- | --- |
| [`registry/proxy/proxyregistry.go`](https://github.com/distribution/distribution/blob/v3.1.2/registry/proxy/proxyregistry.go) | One RemoteURL per app; default transport use | Instance options inject validated transport and context, plus bounded dispatcher; prove same repo at two origins cannot collide. |
| [`registry/proxy/proxyauth.go`](https://github.com/distribution/distribution/blob/v3.1.2/registry/proxy/proxyauth.go) | Discovery and ping use http.Get, including constructor paths; host/eTLD checks are not SSRF defense | Inject safe discovery/token clients, defer mandatory network discovery, allow safe anonymous cross-domain realms; test constructor outage and all token redirects. |
| [`registry/proxy/proxytagservice.go`](https://github.com/distribution/distribution/blob/v3.1.2/registry/proxy/proxytagservice.go) | Local fallback on any upstream error, no freshness indicator | Preserve typed cause, allow only availability/complete-closure fallback, publish digest/outcome; test deny/notfound/TLS failures never stale. |
| [`registry/proxy/proxyblobstore.go`](https://github.com/distribution/distribution/blob/v3.1.2/registry/proxy/proxyblobstore.go) | Writes before global admission; package-global inflight by digest and detached work | Instance-owned shared coordinator, reservations before Create/Put, lease/dedup and cancellation, bounded no-retention path; test concurrent cancellation and oversized blobs. |
| [`registry/proxy/proxymanifeststore.go`](https://github.com/distribution/distribution/blob/v3.1.2/registry/proxy/proxymanifeststore.go) | Remote manifests write via local Put | Apply all-write guard and complete-image closure checks; test manifests cannot bypass budget. |
| [`internal/client/repository.go`](https://github.com/distribution/distribution/blob/v3.1.2/internal/client/repository.go), [`auth/session.go`](https://github.com/distribution/distribution/blob/v3.1.2/internal/client/auth/session.go) | Nested clients/redirect callbacks | Audit every client and auth forwarding; transport validates every actual dial regardless of callback, no inherited environment proxy/keychain. |
| `registry/handlers`, storage driver and proxy scheduler | Global health/metrics registration, TTL expiration and upload cleanup | Own one health lifecycle, disable competing deletion; per-upstream metadata namespace with shared blob CAS and managed cleanup. |

Global digest storage plus upstream-isolated repository links avoids duplicate accounting. Separate complete filesystem roots per upstream would duplicate layers, so are not the proposed solution. Keep modifications covered by focused regression tests against the exact upstream base; refresh patch inventory on every dependency update.

## R2 — Original-reference guest pulls and Dockerfile builds

**Decision**: Validate the existing pinned engine's containerd image store and `_default/hosts.toml`; retain the Buildx docker driver using embedded BuildKit. Use original `ns` to choose upstream, never project rewrites or TLS interception.

**Rationale and primary evidence**:

- [Moby Docker 29.9.3 RegistryHosts](https://github.com/moby/moby/blob/docker-v29.9.3/daemon/hosts.go#L24-L49) reads Docker's [certs directory](https://github.com/moby/moby/blob/docker-v29.9.3/daemon/pkg/registry/config.go#L62-L78) using containerd ConfigureHosts. Legacy daemon registry-mirrors remains Docker-Hub-only.
- Pinned containerd 2.4.2 has [`_default` host lookup](https://github.com/containerd/containerd/blob/v2.4.2/core/remotes/docker/config/config_unix.go#L22-L31) and [original namespace query](https://github.com/containerd/containerd/blob/v2.4.2/core/remotes/docker/resolver.go#L601-L606). Its [hosts guide](https://github.com/containerd/containerd/blob/v2.4.2/docs/hosts.md#L177-L198) distinguishes mirror-with-parent fallback from cache-as-server routing. Prove ns, capabilities and mTLS in both modes on the pinned engine.
- [Moby initBuildkit](https://github.com/moby/moby/blob/docker-v29.9.3/daemon/command/daemon.go#L431-L470) passes RegistryHosts into the [BuildKit worker](https://github.com/moby/moby/blob/docker-v29.9.3/daemon/internal/builder-next/controller.go#L148-L155). This is a source-backed proposed common resolver, not evidence that an actual ShiftBox build used it.
- [Docker driver](https://docs.docker.com/build/builders/drivers/docker/) uses embedded BuildKit and does not accept arbitrary BuildKit parameters. Separate [BuildKit mirror configuration](https://docs.docker.com/build/buildkit/configure/) configures a separate builder; it is not automatically needed or sufficient here.
- Dev Container CLI 0.90.0 [uses buildx when available](https://github.com/devcontainers/cli/blob/v0.90.0/src/spec-node/singleContainer.ts#L159-L187). Its [image inspection](https://github.com/devcontainers/cli/blob/v0.90.0/src/spec-node/utils.ts#L260-L285) may query upstream metadata on local miss. Use CLI-resolved config and pre-pull original refs before that path where necessary; do not hand-parse JSONC or assume Docker mirrors intercept Node requests.

**Alternatives considered**: A Docker-Hub-only registry-mirrors setting fails any-public scope. Unproven separate docker-container builder service, reference rewriting and TLS interception are not authorized fallbacks. Failure of the embedded path is a technical validation stop, not permission to weaken transparency.

## R3 — Storage and freshness are ShiftBox responsibilities

**Decision**: One all-write admission/LRU coordinator, shared physical CAS, current-volume checks, active leases and no-retention streaming. Tag requests revalidate; complete cached images can survive only classified availability failures. Keep immutable running digest separate from upstream verification.

**Rationale**: Stock Distribution TTL is neither a hard byte budget nor a global LRU and cannot protect a shared installation volume while downloading. Its broad tag fallback cannot produce the requested warning/error semantics. HTTP status alone does not prove a complete cached selected-platform image.

**Alternatives considered**: Evict-after-download violates disk protection. Generic stale-on-any-error hides denials. Direct fallback for restricted guests violates policy. No-retention controller streaming reconciles evict-before-write with restricted image access without introducing a general proxy.

## R4 — Repository evidence and operator UI

**Decision**: Add a Registry cache tab to `/infrastructure/?section=registry-cache` using the existing Administration → Infrastructure navigation and permission actions.

**Inspected files**: `internal/app/guest_server.go` uses a shared 20-second read/write timeout. `internal/guest/devcontainer.go` invokes `/usr/local/bin/devcontainer` with `/usr/bin/docker`; `devcontainer_timing.go` tracks phases. `platform/packer/install-toolchain.sh` pins Docker/containerd/Buildx/CLI and preloads an ARM64 MCR base. `platform/terraform/aws/services.tf` mounts controller installation at `/var/lib/shiftbox` from `/var/lib/shiftbox-data/shiftbox`, PostgreSQL separately under `/var/lib/shiftbox-data/postgres`, each with a 512 MiB memory reservation. They share the deployment data hierarchy; this is repository configuration, not inspected live capacity. Bound memory and measure actual mount statfs. Do not expand cloud resources without a reviewed Terraform need.

`ui/features/admin/infrastructure.tsx` uses permission-gated tabs (Platform, Certificates, Maintenance, Secrets); `ui/lib/use-infrastructure.ts` scopes queries to the current user. `internal/web/routes.go` and `management.go` bind authorized endpoints. `specs/001-shiftbox-mvp/contracts/openapi.yaml` is the current canonical API definition; new feature contracts here do not mutate it during planning.

Mantine MCP was consulted for component selection on this date: Stepper accessibility, Chip accessibility/read-only behavior and form/input search. [Stepper](https://mantine.dev/core/stepper/) uses labeled buttons; [Chip](https://mantine.dev/core/chip/) is a checkbox/radio primitive, so status chips must be read-only without implying a setting. Use Switch, TextInput, NumberInput, Button, Alert, Progress, Paper and Tabs with existing Carbon styling and `branding/` assets. Explicit save for the grouped form; no mixture with toggle autosave. Do not copy generic Radix/shadcn skill examples into this Mantine application.

**Alternatives considered**: Backend-only settings, a separate navigation hierarchy and unrelated cache management actions are unnecessary. The [coverage matrix](contracts/admin-ui.md) makes UI completeness reviewable.

## R5 — License and release packaging

**Decision**: Keep ShiftBox MIT; Distribution and its modifications retain Apache-2.0 obligations. Inventory the actual linked dependency graph when the module is added; ship licenses, applicable notices/attribution and modified-file notices in binary and container packaging.

**Rationale**: [Distribution license §4](https://github.com/distribution/distribution/blob/v3.1.2/LICENSE#L79-L115) permits overall-work terms with redistribution conditions preserved; [ASF FAQ](https://www.apache.org/foundation/license-faq.html#Distribute-changes) describes redistribution. The inspected v3.1.2 tree has no root NOTICE; this says nothing about linked dependency NOTICE files. This is an engineering packaging plan, not an unqualified legal certification.

**Alternatives considered**: Relabeling Apache code MIT and omitting transitive notices are not acceptable. Do not change ShiftBox's top-level MIT license.

## Planning Closure

No required product decision remains open. Engineering gates G1–G7 in the plan have explicit test tasks; none has passed runtime validation. If the chosen supported toolchain cannot deliver transparent any-public pull/build routing or safe no-retention streaming, report that specific technical blocker before implementation release; do not silently substitute a weaker design.

## Scope reduction for quick implementation

The final plan has 24 tasks and six combined validation checks. Reuse installation settings, operation/workspace summaries, guest trust, embedded Docker BuildKit and current Mantine admin patterns. Keep one shared cache HTTP/DNS/storage fixture plus existing guest/API/UI suites. No arbitrary pool/timeout tuning interface, separate activation/telemetry/retry subsystem, dedicated cache volume/quota project, exhaustive protocol/performance matrix or one test file per failure variant. These were larger-draft engineering proposals, not user requirements, and are removed. Required public-only transport, registered-guest checks, original refs, pre-write reservations/LRU, stale recovery and UI persistence remain; the Distribution adaptation and actual Docker/build routing still need the focused proof before support can be claimed.

T004 verified official v3.1.2 release commit `3220848f15d9279c66a41aa0a257469e6aede1e9` and pinned it in go.mod/go.sum. Go 1.27.4 meets the release minimum. See the [integration inventory](../../third_party/distribution/patches/README.md) for the stock proxy boundaries and Apache license. Linked-dependency notice verification follows the final binary build.
