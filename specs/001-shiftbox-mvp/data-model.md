# ShiftBox MVP Data Model

The logical model applies to both SQLite and PostgreSQL. Physical migrations may vary by dialect, but identifiers, constraints, state transitions, and transaction boundaries remain equivalent. All timestamps are UTC. Every mutable aggregate has a monotonically increasing `version` for conditional API mutation.

## Installation and configuration

### Installation

Represents the single logical ShiftBox installation.

| Field | Meaning and validation |
| --- | --- |
| `id` | Stable opaque UUID; exactly one active installation record |
| `state` | `bootstrap`, `configuring`, `active`, `recovery`, or `blocked` |
| `organization_name` | Non-empty display name |
| `data_profile` | `sqlite` or `postgres`; immutable after active configuration except guarded restore activation |
| `management_origin` | Canonical HTTPS origin; unique and distinct from IDE/preview patterns |
| `ide_host_pattern` | Validated host pattern that maps exactly one workspace ID |
| `preview_host_pattern` | Validated host pattern that maps workspace and service identity |
| `external_auth_interval_seconds` | Positive finite value; initial default 14,400 seconds |
| `active_release` | Current controller product release |
| `min_guest_release` | Immediately previous supported guest release |
| `config_generation` | Increments on durable configuration activation |
| `created_at`, `updated_at` | Audit timestamps |
| `version` | Optimistic-concurrency version |

**State transitions**:

```text
bootstrap -> configuring -> active
configuring -> bootstrap       (safe preactivation failure only)
active -> recovery             (restore activation)
active -> blocked              (unsafe migration/key/config failure)
recovery -> active             (administrator review + provider reconciliation)
blocked -> recovery            (validated recovery procedure)
```

### ActivatedConfiguration

Records durable configuration generations without storing transient privileged setup credentials.

| Field | Meaning and validation |
| --- | --- |
| `installation_id`, `generation` | Composite identity; generation increases exactly by one |
| `settings_json` | Non-secret normalized settings and protected-value references |
| `activated_by` | User ID or protected local setup actor |
| `activated_at` | Activation timestamp |
| `retired_at` | Set when superseded; the last active generation is unique |

### BrowserCertificate

Tracks uploaded certificate metadata and protected private-key reference.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `chain_pem` | Parsed certificate chain; covers every configured service hostname |
| `private_key_ciphertext_id` | Protected-value record; never returned by management APIs |
| `not_before`, `not_after` | Parsed validity interval |
| `fingerprint_sha256` | Unique certificate fingerprint |
| `state` | `pending`, `active`, `replaced`, or `invalid` |

Only a validated certificate/key pair can become active. A replacement is activated atomically with configuration.

## People, identity, and authorization

### User

Application principal separate from Dex credential records.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `primary_email` | Normalized email for display/local login; not a cross-provider merge key |
| `display_name` | Optional user-visible name |
| `role` | `member` or `administrator` |
| `membership_state` | `pending`, `active`, or `disabled` |
| `credential_mode` | `local` or `external` |
| `must_change_password` | True for temporary local credentials |
| `external_authorized_at` | Last successful actual upstream authorization; null for local users |
| `created_by`, `created_at`, `updated_at` | Provenance |
| `version` | Conditional updates |

### IdentityBinding

Links stable OIDC identity to a user.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `user_id` | User reference |
| `issuer` | Exact canonical issuer string |
| `subject` | Exact non-empty provider subject |
| `connector_id` | Dex/upstream connector identity |
| `email_at_link` | Historical profile value only |
| `linked_by`, `linked_at` | Administrator-approved provenance |

`(issuer, subject, connector_id)` is unique. Email never creates a binding automatically.

### Session

Server-side ShiftBox browser session.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID visible only as server record identity |
| `token_digest` | Unique digest of a random cookie value; raw token is never stored |
| `user_id` | User reference |
| `mode` | `bootstrap_restricted`, `password_change`, `normal`, or `recovery_review` |
| `csrf_digest` | Digest of session-bound mutation token |
| `authenticated_at`, `external_authorized_at` | Authentication clocks |
| `expires_at`, `revoked_at`, `revocation_reason` | Lifecycle |

A session is usable only when user membership is active, mode permits the action, time bounds pass, and the installation gate permits it.

### ResourceGrant

Explicit resource/action permission beyond predefined role defaults.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `subject_user_id` | Grantee |
| `resource_type`, `resource_id` | Workspace or template identity |
| `action` | Enumerated permission such as `workspace.connect_admin` |
| `granted_by`, `granted_at`, `revoked_at` | Audited lifecycle |

Only active grants participate in authorization. Administrator connection to another owner's workspace requires a distinct active grant and creates an audit event.
The complete action vocabulary, predefined role defaults, ownership rules, and session/installation gates are defined by [contracts/authorization-matrix.md](./contracts/authorization-matrix.md); database values outside that vocabulary are rejected.

## Platform, templates, and secrets

### PlatformArtifact

Immutable administrator-imported VM platform release.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `release_id` | Unique human-readable release identity |
| `architecture` | Must be `amd64` for MVP |
| `platform_version`, `guest_release`, `protocol_version` | Compatibility metadata |
| `owned_resources_json` | Complete expected disk/media inventory |
| `digests_json` | Required content digests |
| `provenance_json` | Administrator-reviewed source metadata |
| `imported_by`, `imported_at` | Provenance |
| `is_default` | At most one compatible default |
| `state` | `validating`, `available`, or `rejected` |

Available artifact content and manifest fields are immutable. Removing a catalog default does not mutate existing workspaces.

### ResourceProfile

Administrator-defined T-shirt size.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `name` | Unique, non-empty name |
| `cpu_count`, `memory_mib`, `disk_mib` | Positive values inside provider capability bounds |
| `enabled` | Controls future selection only |
| `version` | Profile revision |

### CapacityBudget

| Field | Meaning and validation |
| --- | --- |
| `installation_id` | Single installation |
| `cpu_limit`, `memory_mib_limit`, `disk_mib_limit` | Positive configured totals |
| `updated_by`, `updated_at`, `version` | Audited changes |

### ProjectTemplate

Mutable template identity with immutable revisions.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `owner_user_id` | Owner; administrator management does not silently transfer ownership |
| `name` | Non-empty display name |
| `state` | `active` or `archived` |
| `current_revision` | Existing immutable revision number |
| `created_at`, `updated_at`, `archived_at` | Lifecycle |
| `version` | Conditional template mutation |

### TemplateRevision

| Field | Meaning and validation |
| --- | --- |
| `template_id`, `revision` | Composite immutable identity |
| `repository_url` | HTTPS GitHub.com URL in MVP |
| `repository_ref` | Optional branch/tag/commit request |
| `devcontainer_path` | Normalized repository-relative path |
| `resource_profile_id` | Enabled profile at creation time |
| `platform_artifact_id` | Compatible available artifact |
| `network_policy_id` | Supported provider policy |
| `secret_names_json` | References only; never plaintext values |
| `created_by`, `created_at` | Provenance |

### TemplateGrant

| Field | Meaning and validation |
| --- | --- |
| `template_id`, `user_id` | Unique active grant pair |
| `can_launch` | True for MVP grants |
| `project_secret_warning_ack_at` | Records that the grantor saw the exposure warning |
| `granted_by`, `granted_at`, `revoked_at` | Lifecycle |

### SecretValue

Protected application secret at exactly one supported scope.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `scope_type` | `user`, `template`, or `organization` only |
| `scope_id` | User/template/installation identity matching scope type |
| `name` | Normalized non-empty key; unique within active scope |
| `ciphertext`, `nonce`, `key_version` | Authenticated encrypted value |
| `is_explicit_empty` | Distinguishes empty override from absence |
| `created_by`, `created_at`, `updated_at`, `version` | Audited metadata |

Resolution order is User → Project Template → Organization. Deletion reveals the next authorized lower value. The value itself never appears in audit or management read models.

### GitAuthorization

| Field | Meaning and validation |
| --- | --- |
| `user_id` | One record per user and app registration |
| `provider` | `github.com` in MVP |
| `provider_user_id` | Stable GitHub identity |
| `access_ciphertext_id`, `refresh_ciphertext_id` | Protected-value references |
| `access_expires_at`, `refresh_expires_at` | Provider clocks |
| `state` | `connected`, `refreshing`, `reconnect_required`, or `revoked` |
| `refresh_generation` | Fences concurrent refresh results |
| `updated_at`, `version` | Lifecycle |

## Workspace lifecycle and operations

### Workspace

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID and provider ownership tag |
| `owner_user_id` | Immutable owner context |
| `template_id`, `template_revision` | Captured provenance |
| `platform_artifact_id` | Immutable platform selection |
| `repository_url`, `repository_ref`, `devcontainer_path` | Captured launch configuration |
| `cpu_count`, `memory_mib`, `disk_mib` | Pinned resolved resources |
| `network_policy_snapshot_json` | Verified effective policy |
| `desired_state` | `stopped`, `running`, or `deleted` |
| `observed_state` | `unknown`, `provisioning`, `stopped`, `starting`, `running`, `unreachable`, `stopping`, `deleting`, `deleted`, or `failed` |
| `generation` | Increments for conflicting or identity-invalidating lifecycle changes |
| `provider_instance_id` | Null until reconciled creation; unique while live |
| `guest_release`, `protocol_version` | Last authenticated report |
| `retention_deadline`, `deletion_commit_at` | Retention authority clocks |
| `last_activity_at`, `last_contact_at` | Controller-owned observed clocks |
| `created_at`, `updated_at`, `version` | Lifecycle |

**Desired-state transitions**:

```text
stopped -> running
running -> stopped
stopped -> deleted
running -> deleted    (explicit/retention flow first creates stop/delete operation)
deleted -> no transition
```

Observed state never changes solely because desired state changed. Completion requires provider and guest evidence defined by the lifecycle contract.

### Reservation

| Field | Meaning and validation |
| --- | --- |
| `workspace_id` | Unique active reservation per workspace |
| `cpu_count`, `memory_mib`, `disk_mib` | Match workspace pinned allocation |
| `cpu_memory_state` | `reserved` or `released` |
| `disk_state` | `reserved` or `released` |
| `reserved_at`, `cpu_memory_released_at`, `disk_released_at` | Evidence clocks |

CPU/memory release requires provider-confirmed stop or deletion. Disk release requires verified complete deletion. Unknown outcomes retain reservations.

### Operation

Durable idempotency and workflow record.

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID returned for idempotent request |
| `workspace_id` | Optional for installation-wide operations |
| `type` | `provision`, `start`, `open_ide`, `rebuild`, `repair`, `stop`, `delete`, `recover`, `update`, `backup`, `restore`, or setup operation |
| `generation` | Workspace/config generation to which operation applies |
| `requested_by`, `request_key` | Actor and unique idempotency key |
| `status` | `pending`, `running`, `waiting`, `succeeded`, `failed`, or `cancelled` |
| `current_step` | Stable step key |
| `error_code`, `error_detail` | Stable public code and redacted actionable detail |
| `lease_owner`, `lease_expires_at` | Renewable worker claim |
| `created_at`, `started_at`, `finished_at`, `version` | Lifecycle |

`(requested_by, request_key)` is unique. Repeated requests return the existing logical operation.

### OperationStep and OperationEvent

| Field | Meaning and validation |
| --- | --- |
| `operation_id`, `step_index` | Ordered stable identity |
| `step_key`, `label` | Contract key and user-facing label |
| `status` | `pending`, `active`, `succeeded`, `failed`, or `skipped` |
| `started_at`, `finished_at`, `error_code`, `detail` | Progress |
| `event_id` | Monotonic per operation; used for SSE replay |

Steps are append/update-only workflow evidence. Completed steps are never silently removed when a retry resumes.

### ProviderResource

| Field | Meaning and validation |
| --- | --- |
| `workspace_id`, `provider_resource_id` | Composite identity |
| `resource_type` | `vm`, `disk`, `media`, `network`, or adapter-defined owned type |
| `generation` | Owning workspace generation |
| `observed_json` | Normalized provider facts |
| `ownership_proof` | Tags/manifest evidence used before mutation |
| `last_observed_at`, `deleted_at` | Reconciliation evidence |

Deletion succeeds only when all inventoried owned resources are confirmed absent and shared resources remain.

### NetworkPolicy

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `name` | Unique display name |
| `mode` | `all`, `no_network`, or `allowlist` |
| `rules_json` | Destination CIDR, transport, and destination port entries |
| `required_exceptions_json` | Controller/proxy, DNS, Git, and approved auxiliary destinations |
| `provider_capability_version` | Capability evidence used to admit policy |
| `enabled`, `version` | Lifecycle |

### ActivityPolicy and ActivityObservation

`ActivityPolicy` selects IDE-connection and/or CPU monitors and defines thresholds. `ActivityObservation` records controller receipt time, source, active/idle result, and guest generation. Any selected active signal resets the deadline; an idle report does not.

## Guest trust and proxy access

### GuestRegistration

| Field | Meaning and validation |
| --- | --- |
| `workspace_id`, `generation` | Identity scope |
| `state` | `eligible`, `active`, `expired`, `revoked`, or `deleted` |
| `certificate_serial`, `certificate_not_after` | Current issued leaf metadata |
| `public_key_fingerprint` | Current guest key proof |
| `last_authenticated_at`, `revoked_at`, `revocation_reason` | Lifecycle |

Revoked/deleted registrations cannot automatically re-enroll. Expiry is diagnostic, not authority.

### EnrollmentGrant

| Field | Meaning and validation |
| --- | --- |
| `id` | Random high-entropy identifier stored as a digest |
| `workspace_id`, `generation`, `operation_id` | Exact binding |
| `provider_instance_id` | Verified stopped VM binding |
| `expires_at` | Initially 15 minutes after minting |
| `state` | `issued`, `consumed`, `expired`, or `invalidated` |
| `key_proof` | Set atomically on consumption for safe same-transaction retry |

Single-use consumption and certificate issuance occur in one transaction. A different key proof never receives a replayed result.

### OriginGrant

Short-lived IDE/preview browser authority.

| Field | Meaning and validation |
| --- | --- |
| `id`, `token_digest` | Opaque grant identity |
| `user_id`, `workspace_id`, `service_key` | Exact user/resource/service binding |
| `origin` | Exact destination origin |
| `permission_basis` | Owner or audited administrator grant |
| `expires_at`, `consumed_at`, `revoked_at` | Lifecycle |

### TunnelSession

Ephemeral state, with only connection metadata persisted when needed for audit/reconciliation: workspace/generation, certificate serial, connected time, last heartbeat, proxy node identity, and closed reason. It never becomes provider truth.

## Maintenance, notices, audit, and recovery

### MaintenanceCampaign and MaintenanceTarget

Campaign fields: release ID, administrator-selected deadline, created actor/time, state (`scheduled`, `active`, `blocked`, `completed`, `cancelled`). Target fields: workspace, prior desired/observed state, operation ID, state (`pending`, `waiting_safe`, `updating`, `verified`, `restoring_state`, `failed`, `completed`), error, and timestamps.

Maintenance does not count as user activity. A failed or unsafe target blocks campaign completion and the next controller advance; it is never force-killed by maintenance authority.

### Notice and NoticeReceipt

Notice fields: ID, type (`system`, `maintenance`, `certificate`, `storage`, `recovery`), severity, title/body, effective/expiry times, creator, and persistent condition key. Receipt records user, first read time, and dismissed time. Persistent operational notices remain visible while their condition is active regardless of dismissal history.

### AuditEvent

| Field | Meaning and validation |
| --- | --- |
| `id`, `occurred_at` | Ordered identity/time |
| `actor_type`, `actor_id` | User, guest, controller, or protected local recovery actor |
| `action` | Stable action key |
| `resource_type`, `resource_id` | Context |
| `outcome`, `reason_code` | Result without secret material |
| `metadata_json` | Redacted bounded context; schema rejects known secret fields |

Audit retention never removes current authorization, operation, maintenance, or workspace provenance records.

### BackupRecord

| Field | Meaning and validation |
| --- | --- |
| `id`, `operation_id` | Identity and visible workflow |
| `format_version`, `product_release`, `schema_versions_json` | Compatibility |
| `state` | `preparing`, `checkpointing`, `encrypting`, `completed`, or `failed` |
| `manifest_digest`, `archive_digest`, `size_bytes` | Verification metadata only |
| `created_by`, `created_at`, `completed_at`, `error_code` | Lifecycle |

The password and archive plaintext are never stored. A record is `completed` only after archive close and integrity verification.

### RecoveryReview

| Field | Meaning and validation |
| --- | --- |
| `id` | UUID |
| `restore_operation_id` | Source restore |
| `state` | `identity_review`, `provider_reconcile`, `authorization_review`, `ready`, `activated`, or `blocked` |
| `reviewed_users_json`, `reconciled_workspaces_json` | Decisions/evidence, not copied authority |
| `started_by`, `approved_by`, `started_at`, `activated_at` | Audited lifecycle |

Normal mutations, user workspace access, and overdue destructive retention remain gated until the review reaches `activated`.

## Transaction invariants

1. Authorization is checked in the same transaction that creates durable intent or changes authority.
2. Workspace generation and operation generation must match before any result can update observed state.
3. Reservations are created before provider start/create and released only with confirmed evidence.
4. A secret resolver derives owner/template/organization from controller records; guest-supplied IDs never select scope.
5. Session/grant revocation commits before connection close notifications are emitted.
6. Operation success and its completion evidence commit atomically.
7. External provider, guest, GitHub, and Dex calls never occur while holding a database transaction open; pending intent is committed first and reconciled afterward.
8. Restore activation occurs only after archive validation, target ownership validation, protected-key validation, and creation of a RecoveryReview gate.
