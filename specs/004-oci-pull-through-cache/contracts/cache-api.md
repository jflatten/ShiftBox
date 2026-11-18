# Proposed API and guest OCI contract

These are design contracts for later implementation. Existing canonical OpenAPI is `specs/001-shiftbox-mvp/contracts/openapi.yaml`; do not edit it until contract-test-first implementation. Management paths below include `/api/v1`; use current authorized handler/Problem/CSRF/version conventions.

## Management API

| Method/path | Operation ID | Permission | Contract |
| --- | --- | --- | --- |
| GET `/api/v1/admin/image-cache` | `getImageCache` | `infrastructure.view` + active administrative context | Settings, desired/effective versions, activation, health, usage and effectiveness snapshot. |
| PUT `/api/v1/admin/image-cache` | `updateImageCache` | `infrastructure.manage` + active administrative context | Full replacement settings, current If-Match, Origin/CSRF. Validate then stage activation; audit result. |

GET response shape (illustrative, fields normative):

```json
{
  "version": 1,
  "desired": {"enabled": false, "storagePath": "/var/lib/shiftbox/cache/registry", "maxBytes": null, "volumeHighPercent": 80, "volumeLowPercent": 70},
  "effective": {"version": 1, "enabled": false, "storagePath": "/var/lib/shiftbox/cache/registry", "maxBytes": null, "volumeHighPercent": 80, "volumeLowPercent": 70},
  "activation": {"phase": "disabled", "error": null},
  "health": {"state": "disabled", "observedAt": null, "detail": null, "guestEndpoint": null, "capabilities": []},
  "usage": null,
  "effectiveness": null,
  "retiredLocations": [],
  "cachedImages": []
}
```

Path/endpoint defaults are expanded by the server, never hardcoded to the demo installation in UI. Health states include `ready`, `disabled`, `degraded`, `unavailable`, `pressure`, `unknown`. Each has a human-readable reason and timestamp. Optional capability failures identify unsupported guest tooling and relevant launch diagnostics without exposing another owner's data.

PUT body is the five `desired` fields. `maxBytes` is a positive decimal-string integer when enabled; server signed-64-bit bound, UI whole-MiB conversion. `volumeLowPercent`/`volumeHighPercent` are integers satisfying `0 < low < high < 100`. Only safe absolute cache paths allowed. Unknown fields, registry credentials and upstream policy/allowlist input are rejected. Disabled initial settings may have null budget; enabling cannot.

Return `202` with operation ID/location when drain/activation is asynchronous; display using existing operation events/Stepper. Return `200` with fresh GET shape for an already-effective/no-op save. Missing/stale version follows existing 428/412 conventions; activation already in progress returns 409 with current operation reference. Invalid input/path returns 400 with safe field-level Problem details, unavailable storage 503 where transient. Never return success with hidden effective failure. Effective activation error is durable and available on GET/operation stream after reload. No long filesystem/network work in authorization SQL transactions: stage a fenced command and reconcile through the existing operation service.

All responses use no-store, current session authority and stable safe codes such as `image_cache.invalid_settings`, `image_cache.path_unavailable`, `image_cache.capacity_unavailable`, `image_cache.activation_failed`. Follow existing application conventions if exact numeric status differs; lock it in tests/OpenAPI before implementation. Audit settings changes, activation failures and denied access without tokens, URI query credentials or secrets. GET refresh is read-only, not a hidden purge or upstream verification command. Save is the only new admin mutation; no purge, push, credentials, arbitrary upstream or retry-button API is introduced.

### Cached images

The existing authorized GET includes up to 100 recent retained references in `cachedImages`. Each row has `reference`, `digest`, optional `architecture` and `availability` (`complete` or `partial`). Coverage is for the recorded platform when platform selection applies. The controller checks retained manifests and repository links without contacting upstream or changing eviction order. No per-image byte totals, writes or additional endpoints are exposed.

### Usage/effectiveness fields

All byte quantities are exact decimal strings; counts/timestamps follow existing JSON conventions.

- Usage: `retainedBytes`, `temporaryBytes`, `reservedBytes`, `retiredBytes` (subset of retained total), `budgetBytes`, `volumeTotalBytes`, `volumeUsedBytes`, `volumeAvailableBytes`, `activeTransfers`, `lastEvictionAt`, `lastEvictedBytes`, `pressureReason`, `observedAt`. Unknown values are null, never zero.
- Effectiveness: window start/end, controller cache bytes C, upstream image bytes U, upstream metadata/token bytes M, no-retention upstream bytes S (subset of U), direct guest payload bytes D if known, guest-local bytes L if known, source/provenance and counts of each pull classification.
- Controller image payload reuse percentage is `100*C/(C+U)`, null if denominator zero. Metadata M is excluded; a metadata-only tag check with C > 0 is byte-warm. Retransferred image payload is counted where transferred and denominators are labeled; do not mix compressed network bytes with extracted filesystem sizes. Display C/U and S separately; S must not be counted twice.
- Overall known-source image reuse may display `100*(C+L)/(C+L+U+D)` only if all four quantities are measured on the same basis. Otherwise show “Not measured” for the missing values and omit that overall ratio. Guest-local reuse is not controller effectiveness. Direct fallback is never counted as cache hit.

## Workspace and operation views

Extend existing authorized workspace detail with an `imageCache` summary: original references/platforms, resolved/running/last-verified digests where known, verification time/outcome, `warningActive`, `warningMessage`, `updateAvailable`, per-source byte summary and capability/fallback reason. Owner or existing authorized workspace viewer only; no installation filesystem path or other workspace pulls. Preserve existing Running status independently.

When `warningActive`, use exact text: **The upstream registry could not be contacted. Using cached version.** It persists in launch progress and workspace detail until successful upstream verification. A changed tag can clear that availability warning after verification and separately show “A newer upstream image is available; this workspace is still using its recorded image.” Do not alter running digest or pretend a rebuild occurred. Denial/not-found/TLS/security results are visible failures and cannot authorize stale use on a new pull.

Operation events add bounded image preparation progress: resolve/check → pull or stream → image ready. Use existing workflow event stream rather than one event per chunk. Cache owns trusted byte/freshness observations; guest/CLI phase timings are identified separately. A pull finishing is not proof that browser IDE readiness passed.

## Guest configuration and identity

Extend optional authenticated desired-state/poll data with effective configuration generation, cache endpoint, retention state and effective network fallback mode. No signing keys/controller credentials in payload. Cache CA/current guest client leaf/key remain guest-owned existing identity material. Capability acknowledgement records image-store mode, actual builder/versions and applied config generation; gate enabled use if unsupported.

Every OCI request authenticates current enrollment/VM generation via mTLS. Controller derives workspace and current operation from server authority, not request IDs supplied by guest. Enforce leaf expiry/revocation during persistent transfers and reload identity for future connections after renewal. Image-only access does not authorize enrollment, admin APIs, arbitrary tunnels or a general HTTP CONNECT proxy.

## Read-only OCI endpoint

- Mount `GET`/`HEAD /v2/`, `/v2/<repository>/manifests/<tag-or-digest>`, `/v2/<repository>/blobs/<digest>` with required OCI media types/ranges/HEAD/redirect behavior. Multiarch index and manifest/config bytes participate in admission and completeness. Reject pushes, upload routes, deletes, catalog enumeration and arbitrary methods.
- Resolve original public registry from resolver-provided `ns`, validate canonical authority/repository/reference and ensure mapping cannot collide. Docker Hub shorthand normalization is tested. `ns` is never an arbitrary URL or authority override for token/blob requests.
- Guest-only HTTPS; browser management cookies do not authorize it. No end-user Docker credentials accepted/forwarded. Public anonymous token challenges use only validated public endpoints and pull scopes.
- Server handles upstream redirects internally through guarded clients; do not redirect restricted guests directly to public blob storage, private endpoints or token services. No-retention streaming uses the same guard and bounded memory, never cache temp files.
- Tags revalidate on each pull/tag resolution. Exact digests can reuse complete verified immutable content. A stale tag may select only the previous verified selected-platform closure after a classified availability failure, while maintaining leases until that pull's content is served.
- Upstream availability: DNS/connect failure, timeout, 5xx; 429 is rate-limited, honors Retry-After within the operation deadline and is not stale eligible. Denial/not-found/TLS/digest/security failures are not stale eligible. Missing closure fails rather than combining a nonexistent fallback image.
- Cache capacity/I/O failure yields no-retention streaming when safe. Controller unavailable or streaming impossible yields a structured cache failure. Guests may use original upstream only if existing effective egress allows. Never send a cache error that silently changes restricted policy.
- Per-instance HTTP clients enforce bounded streaming/context behavior and URI/DNS/dial/redirect/token/foreign-blob restrictions, including constructor requests. Never send upstream controller cookies, CA keys or guest client certificates. Public upstream TLS uses normal public server verification.
- On disk overflow, cancel writes before crossing reserved limits, release incomplete files, record pressure and stream/retry safely. No automatic replay of completed lifecycle hooks.
