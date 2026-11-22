# Data Model: reuse existing state

Keep this feature small: extend installation settings and existing operation/workspace records; do not add a cache database, generic configuration service, telemetry store or transfer journal. Select the next available migration in both existing SQL dialects at execution.

## Installation settings

One cache settings object on the installation:

| Field | Value / validation |
| --- | --- |
| `enabled` | Boolean, default false; means content retention/reuse enabled. Disabled may still stream public images through the controller for restricted guests; UI explains it. |
| `storagePath` | Absolute nonempty safe owned cache directory; default `<installationDir>/cache/registry`. Cannot overlap protected DB/identity/keys/backups/workspace data or escape through symlinks. |
| `maxBytes` | Null until configured; enabling requires **positive whole bytes within signed 64-bit range**. JSON decimal string avoids browser rounding; UI accepts exact whole MiB. No Unlimited. |
| `volumeHighPercent` | Integer, proposed default 80. |
| `volumeLowPercent` | Integer, accepted default 70; **0 < volumeLowPercent < volumeHighPercent < 100**. |
| `version` | Existing monotonic settings version/If-Match convention. |

Use existing settings persistence/audit and operation journal for candidate/effective snapshots, activation status/error and retired paths; no dedicated activation subsystem/table. Validate candidate first, let active pulls finish, activate then record effective settings. Failed validation/activation preserves the previous effective configuration. A moved path begins empty; old managed files remain visible and count against total cache/volume use until operator reclamation is observed. No automatic data move or deletion action.

Use the existing operation phases to expose validate → drain → activate → complete/failed to the UI. On restart reconcile unfinished activation against last durable effective settings before retaining new content. Disabled default preserves current deployments until configured; existing workspaces are not modified by migration.

## Workspace and operation summary

Store the minimum needed freshness summary with existing workspace/operation state: original reference, selected platform, resolved image/base digest, actual running digest if known, last successful upstream verification/time, availability warning and newer-upstream indication. Dockerfile bases are input provenance, not the final locally built image's published identity. Successful verification clears the availability warning without changing the running digest. Owner/existing workspace-view permissions apply.

Reuse existing operation summaries for controller-local/upstream image bytes, metadata bytes, no-retention/direct/guest-local classification and safe error reason. Unknown bytes stay unknown; do not add per-chunk persistent events or a new metrics database. The admin status response combines these summaries with current cache/filesystem observations.

## Disposable filesystem state and in-process coordination

Distribution repository/tag metadata is isolated by canonical HTTPS upstream authority; content digest files are shared and count once per physical copy. A minimal local index tracks digest size/completeness/last use; it is rebuildable from managed files, not authoritative application state. A cached stale image requires its selected-platform manifest/config/layers to be complete.

One in-process coordinator owns current reservations and read/write leases, shared-digest work and LRU eviction. Count cache files, temporary files, unconsumed reservations and retired roots; reserve before each write, including unknown-length data and metadata. Active transfers cannot be evicted. Crash cleanup reconciles incomplete files before admission; do not restore old reservations as live. Reuse one storage fixture to test these concrete behaviors, not a general durable transfer engine.

Back up authoritative installation/workspace/operation state under existing management backup behavior; exclude cache payload/rebuildable index. Restore validates the configured path and starts empty if cache files are absent. Reuse existing N/N−1 guest capability/update handling and recovery/rollback procedures. Cache content never authorizes a guest or blocks unrelated management authority.
