# Data model: Project devcontainer image cache

## Organization built-image policy

- **Fields** — installation ID, enabled flag, policy version, publication generation, and oldest pending purge cutoff.
- **Defaults** — new MVP organizations start enabled. Fresh shared storage settings have a positive bounded budget. An invalid or unavailable configured root produces actionable storage status and the normal uncached launch.
- **Transitions** — On → Off commits the setting and advances the generation. This fences the former generation and records cleanup work. Off → On activates a fresh generation. Repeated saves of the same state are idempotent.
- Use the existing managed root list, including retired locations, to find older built artifacts. Clear a cleanup cutoff only after deletion succeeds or all eligible content is confirmed absent. Retry safely after restart.

## Current project image

- **Identity** — installation, project, target OS and architecture. This tuple has at most one visible image.
- **Fields** — authorized canonical source repository, selected config path, SHA-256 of exact config bytes, OCI manifest digest, storage generation and location, publication type, and timestamps for promotion and reconciliation. The observed origin revision can be recorded for publication diagnostics.
- **Validity** — live policy and project access, matching repository, path and config bytes, and a complete manifest, config and layer closure for the requested platform. A commit or branch is provenance for the build and current-origin check rather than part of the match.
- **Transitions** — absent or invalid entries produce a miss. A complete admitted candidate becomes visible on conditional promotion. An explicit refresh holds the previous matching closure while building its replacement. Removing a required object, changing storage generation, or disabling invalidates eligibility. Keep the current pointer conditional so stale cleanup cannot remove a newer image.

## Publication attempt and transfer grant

- **Fields** — opaque scope, kind (launch build or explicit rebuild), authenticated workspace ID and generation, operation and dispatch identity, installation, project, platform, selected path and config digest, policy generation, expiration, and allowed manifest or staged repository scope.
- **Authority** — derive all identity and permission fields from controller records. A guest supplies candidate-byte identifiers, which the controller validates. A bounded admission can outlive the successful workspace operation. Current guest enrollment, workspace/project access, and policy still apply.
- **Lifecycle** — admitted → receiving → complete → promoted, discarded, or expired. Partial or rejected transfers have no pullable project entry. Reclaim expired staging during reconciliation and restart. A repeated finish for a committed candidate is idempotent. Ordinary matching misses cannot displace a complete matching slot that already won.
- Grant only the manifest closure needed for a pull. An admitted reader can finish fetching the same digest across a normal pointer replacement. Disable and revocation fence the grant.

## Shared registry content

The existing content store tracks digest-addressed bytes, namespace links, and live leases for both cache classes. Account for built-image manifests, link records, reservations, and staged bytes before writing. Digest and length checks precede linking. Lookup authorization is scoped even where two projects or the public mirror share physical bytes. Purge releases old built references and removes exclusively owned, unleased objects. Reconstruct usage on restart and treat missing durable content as a miss, including after restore.
