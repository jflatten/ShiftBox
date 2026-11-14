-- +goose Up
-- One stable installation UUID is activated; generations advance in activation transactions.
CREATE TABLE installations (
 id TEXT NOT NULL PRIMARY KEY CHECK (id ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'),
 singleton INTEGER NOT NULL DEFAULT 1 UNIQUE CHECK (singleton = 1),
 state TEXT NOT NULL CHECK (state IN ('bootstrap','configuring','active','recovery','blocked')),
 organization_name TEXT NOT NULL CHECK (length(trim(organization_name)) > 0),
 data_profile TEXT NOT NULL CHECK (data_profile IN ('sqlite','postgres')),
 management_origin TEXT NOT NULL UNIQUE,
 ide_host_pattern TEXT NOT NULL,
 preview_host_pattern TEXT NOT NULL,
 external_auth_interval_seconds BIGINT NOT NULL DEFAULT 14400 CHECK (external_auth_interval_seconds > 0),
 active_release TEXT NOT NULL,
 min_guest_release TEXT NOT NULL,
 config_generation BIGINT NOT NULL DEFAULT 0 CHECK (config_generation >= 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0),
 CHECK (management_origin <> ide_host_pattern AND management_origin <> preview_host_pattern AND ide_host_pattern <> preview_host_pattern)
);
-- The activation service validates canonical HTTPS names and exact workspace/service mapping.
-- An active profile changes only through guarded restore activation.
CREATE TABLE activated_configurations (
 installation_id TEXT NOT NULL REFERENCES installations(id),
 generation BIGINT NOT NULL CHECK (generation > 0),
 settings_json TEXT NOT NULL,
 activated_by TEXT NOT NULL,
 activated_at TEXT NOT NULL,
 retired_at TEXT,
 PRIMARY KEY (installation_id, generation)
);
CREATE UNIQUE INDEX one_active_configuration ON activated_configurations(installation_id) WHERE retired_at IS NULL;
-- Certificate activation validates the matching key and coverage of every service hostname.
CREATE TABLE protected_values (
 id TEXT PRIMARY KEY,
 value_json TEXT NOT NULL
);
CREATE TABLE browser_certificates (
 id TEXT PRIMARY KEY,
 chain_pem TEXT NOT NULL,
 private_key_ciphertext_id TEXT NOT NULL REFERENCES protected_values(id),
 not_before TEXT NOT NULL,
 not_after TEXT NOT NULL,
 fingerprint_sha256 TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL CHECK (state IN ('pending','active','replaced','invalid'))
);
CREATE UNIQUE INDEX one_active_browser_certificate ON browser_certificates(state) WHERE state = 'active';
CREATE TABLE users (
 id TEXT PRIMARY KEY,
 primary_email TEXT NOT NULL,
 display_name TEXT,
 role TEXT NOT NULL CHECK (role IN ('member','administrator')),
 membership_state TEXT NOT NULL CHECK (membership_state IN ('pending','active','disabled')),
 credential_mode TEXT NOT NULL CHECK (credential_mode IN ('local','external')),
 must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1)),
 external_authorized_at TEXT,
 created_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0)
);
CREATE TABLE identity_bindings (
 id TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id),
 issuer TEXT NOT NULL CHECK (length(issuer) > 0),
 subject TEXT NOT NULL CHECK (length(subject) > 0),
 connector_id TEXT NOT NULL,
 email_at_link TEXT,
 linked_by TEXT NOT NULL,
 linked_at TEXT NOT NULL,
 UNIQUE (issuer, subject, connector_id)
);
CREATE TABLE sessions (
 id TEXT PRIMARY KEY,
 token_digest TEXT NOT NULL UNIQUE CHECK (token_digest ~ '^[0-9a-f]{64}$'),
 user_id TEXT NOT NULL REFERENCES users(id),
 mode TEXT NOT NULL CHECK (mode IN ('bootstrap_restricted','password_change','normal','recovery_review')),
 csrf_digest TEXT NOT NULL CHECK (csrf_digest ~ '^[0-9a-f]{64}$'),
 authenticated_at TEXT NOT NULL,
 external_authorized_at TEXT,
 expires_at TEXT NOT NULL,
 revoked_at TEXT,
 revocation_reason TEXT
);
CREATE TABLE resource_grants (
 id TEXT PRIMARY KEY,
 subject_user_id TEXT NOT NULL REFERENCES users(id),
 resource_type TEXT NOT NULL CHECK (resource_type IN ('workspace','template')),
 resource_id TEXT NOT NULL,
 action TEXT NOT NULL CHECK (
  (resource_type = 'workspace' AND action IN ('workspace.view','workspace.start','workspace.stop','workspace.rebuild','workspace.repair','workspace.delete','workspace.update','workspace.update_policy','workspace.connect_owner','workspace.connect_admin'))
  OR (resource_type = 'template' AND action IN ('template.view','template.edit','template.create_revision','template.share','template.archive'))
 ),
 granted_by TEXT NOT NULL REFERENCES users(id),
 granted_at TEXT NOT NULL,
 revoked_at TEXT
);
CREATE TABLE operations (
 originating_action TEXT NOT NULL DEFAULT '',
 id TEXT PRIMARY KEY,
 workspace_id TEXT,
 type TEXT NOT NULL CHECK (type IN ('provision','start','open_ide','rebuild','repair','stop','delete','recover','update','backup','restore','setup')),
 generation BIGINT NOT NULL CHECK (generation > 0),
 requested_by TEXT NOT NULL,
 request_key TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('pending','running','waiting','succeeded','failed','cancelled')),
 current_step TEXT,
 error_code TEXT,
 error_detail TEXT,
 lease_owner TEXT,
 lease_expires_at TEXT,
 created_at TEXT NOT NULL,
 started_at TEXT,
 finished_at TEXT,
 version BIGINT NOT NULL CHECK (version > 0),
 UNIQUE (requested_by, request_key)
);
CREATE TABLE operation_steps (
 operation_id TEXT NOT NULL REFERENCES operations(id),
 step_index INTEGER NOT NULL CHECK (step_index >= 0),
 step_key TEXT NOT NULL,
 label TEXT NOT NULL CHECK (length(label) > 0),
 status TEXT NOT NULL CHECK (status IN ('pending','active','succeeded','failed','skipped')),
 started_at TEXT,
 finished_at TEXT,
 error_code TEXT,
 detail TEXT,
 PRIMARY KEY (operation_id, step_index),
 UNIQUE (operation_id, step_key)
);
-- Event IDs are allocated monotonically in the same transaction as journal changes.
CREATE TABLE operation_events (
 operation_id TEXT NOT NULL REFERENCES operations(id),
 event_id BIGINT NOT NULL CHECK (event_id > 0),
 operation_version BIGINT NOT NULL CHECK (operation_version > 0),
 event_type TEXT NOT NULL,
 occurred_at TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 PRIMARY KEY (operation_id, event_id)
);

-- Shared security audit is needed before administrator connection admission.
-- Only explicitly modeled non-secret identifiers are accepted as metadata.
CREATE TABLE audit_events (
 id BIGSERIAL PRIMARY KEY,
 occurred_at TEXT NOT NULL,
 actor_type TEXT NOT NULL CHECK (actor_type IN ('user','guest','controller','local_recovery')),
 actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
 action TEXT NOT NULL CHECK (length(action) > 0),
 resource_type TEXT NOT NULL CHECK (length(resource_type) > 0),
 resource_id TEXT NOT NULL CHECK (length(resource_id) > 0),
 outcome TEXT NOT NULL CHECK (length(outcome) > 0),
 reason_code TEXT NOT NULL CHECK (length(reason_code) > 0),
 metadata_json TEXT NOT NULL CHECK (length(metadata_json) <= 2048 AND (jsonb_typeof(metadata_json::jsonb) = 'object' AND (metadata_json::jsonb - 'ownerId' - 'grantId') = '{}'::jsonb AND (NOT metadata_json::jsonb ? 'ownerId' OR jsonb_typeof(metadata_json::jsonb->'ownerId') = 'string') AND (NOT metadata_json::jsonb ? 'grantId' OR jsonb_typeof(metadata_json::jsonb->'grantId') = 'string')))
);
