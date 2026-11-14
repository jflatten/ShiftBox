-- +goose Up
CREATE TABLE platform_artifacts (
 id TEXT NOT NULL PRIMARY KEY,
 -- Unique human-readable release identity
 release_id TEXT NOT NULL UNIQUE CHECK (length(trim(release_id)) > 0),
 -- Must be `amd64` for MVP
 architecture TEXT NOT NULL CHECK (architecture = 'amd64'),
 platform_version TEXT NOT NULL,
 guest_release TEXT NOT NULL,
 protocol_version TEXT NOT NULL,
 owned_resources_json TEXT NOT NULL,
 digests_json TEXT NOT NULL,
 provenance_json TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 manifest_sha256 TEXT NOT NULL CHECK (length(manifest_sha256) = 64 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'),
 bundle_name TEXT NOT NULL,
 imported_by TEXT NOT NULL REFERENCES users(id),
 imported_at TEXT NOT NULL,
 is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
 -- `validating`, `available`, or `rejected`
 state TEXT NOT NULL CHECK (state IN ('validating','available','rejected')),
 CHECK (is_default = 0 OR state = 'available')
);
-- At most one compatible default
CREATE UNIQUE INDEX one_default_platform_artifact ON platform_artifacts(is_default) WHERE is_default = 1;

-- Available content/manifest fields are immutable. The default flag can change.
-- +goose StatementBegin
CREATE TRIGGER immutable_available_platform_artifact
BEFORE UPDATE OF id, release_id, architecture, platform_version, guest_release, protocol_version,
 owned_resources_json, digests_json, provenance_json, manifest_json, manifest_sha256, bundle_name,
 imported_by, imported_at, state ON platform_artifacts
WHEN OLD.state = 'available'
BEGIN
 SELECT RAISE(ABORT, 'available platform artifacts are immutable');
END;
-- +goose StatementEnd

CREATE TABLE resource_profiles (
 id TEXT NOT NULL PRIMARY KEY,
 -- Unique, non-empty name
 name TEXT NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
 -- Positive values inside provider capability bounds
 cpu_count BIGINT NOT NULL CHECK (cpu_count > 0),
 memory_mib BIGINT NOT NULL CHECK (memory_mib > 0),
 disk_mib BIGINT NOT NULL CHECK (disk_mib > 0),
 enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
 version BIGINT NOT NULL CHECK (version > 0)
);

CREATE TABLE capacity_budgets (
 installation_id TEXT NOT NULL PRIMARY KEY REFERENCES installations(id),
 -- Positive configured totals
 cpu_limit BIGINT NOT NULL CHECK (cpu_limit > 0),
 memory_mib_limit BIGINT NOT NULL CHECK (memory_mib_limit > 0),
 disk_mib_limit BIGINT NOT NULL CHECK (disk_mib_limit > 0),
 updated_by TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0)
);

CREATE TABLE workspaces (
 id TEXT NOT NULL PRIMARY KEY,
 installation_id TEXT NOT NULL REFERENCES installations(id),
 -- Immutable owner context
 owner_user_id TEXT NOT NULL REFERENCES users(id),
 template_id TEXT NOT NULL,
 template_revision BIGINT NOT NULL CHECK (template_revision > 0),
 platform_artifact_id TEXT NOT NULL REFERENCES platform_artifacts(id),
 repository_url TEXT NOT NULL,
 repository_ref TEXT,
 devcontainer_path TEXT NOT NULL,
 -- Pinned resolved resources
 cpu_count BIGINT NOT NULL CHECK (cpu_count > 0),
 memory_mib BIGINT NOT NULL CHECK (memory_mib > 0),
 disk_mib BIGINT NOT NULL CHECK (disk_mib > 0),
 network_policy_snapshot_json TEXT NOT NULL,
 secret_names_json TEXT NOT NULL DEFAULT '[]',
 retention_seconds BIGINT CHECK (retention_seconds > 0),
 launch_retention_seconds BIGINT CHECK (launch_retention_seconds > 0),
 -- `stopped`, `running`, or `deleted`
 desired_state TEXT NOT NULL CHECK (desired_state IN ('stopped','running','deleted')),
 -- `unknown`, `provisioning`, `stopped`, `starting`, `running`, `unreachable`, `stopping`, `deleting`, `deleted`, or `failed`
 observed_state TEXT NOT NULL CHECK (observed_state IN ('unknown','provisioning','stopped','starting','running','unreachable','stopping','deleting','deleted','failed')),
 generation BIGINT NOT NULL CHECK (generation > 0),
 ownership_marker TEXT NOT NULL CHECK (length(ownership_marker) > 0),
 -- Null until reconciled creation; unique while live
 provider_instance_id TEXT,
 guest_release TEXT,
 protocol_version TEXT,
 retention_deadline TEXT,
 deletion_commit_at TEXT,
 last_activity_at TEXT,
 last_contact_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0)
);
CREATE UNIQUE INDEX live_workspace_provider_instance ON workspaces(provider_instance_id)
 WHERE provider_instance_id IS NOT NULL AND observed_state <> 'deleted';

CREATE TABLE reservations (
 -- Unique active reservation per workspace
 workspace_id TEXT NOT NULL PRIMARY KEY REFERENCES workspaces(id),
 cpu_count BIGINT NOT NULL CHECK (cpu_count > 0),
 memory_mib BIGINT NOT NULL CHECK (memory_mib > 0),
 disk_mib BIGINT NOT NULL CHECK (disk_mib > 0),
 -- CPU/memory release requires confirmed stop/deletion; disk release requires
 -- verified complete deletion. Unknown outcomes retain reservations.
 cpu_memory_state TEXT NOT NULL CHECK (cpu_memory_state IN ('reserved','released')),
 disk_state TEXT NOT NULL CHECK (disk_state IN ('reserved','released')),
 reserved_at TEXT NOT NULL,
 cpu_memory_released_at TEXT,
 disk_released_at TEXT,
 CHECK ((cpu_memory_state = 'released') = (cpu_memory_released_at IS NOT NULL)),
 CHECK ((disk_state = 'released') = (disk_released_at IS NOT NULL)),
 CHECK (disk_state <> 'released' OR cpu_memory_state = 'released')
);

-- A deleted desired state has no transition. Launch snapshot fields are immutable.
-- +goose StatementBegin
CREATE TRIGGER immutable_workspace_launch
BEFORE UPDATE OF installation_id, owner_user_id, template_id, template_revision, platform_artifact_id,
 repository_url, repository_ref, devcontainer_path, cpu_count, memory_mib, disk_mib, network_policy_snapshot_json, secret_names_json, launch_retention_seconds
ON workspaces
BEGIN
 SELECT RAISE(ABORT, 'workspace launch fields are immutable');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER terminal_workspace_desired_state BEFORE UPDATE OF desired_state ON workspaces
WHEN OLD.desired_state = 'deleted' AND NEW.desired_state <> OLD.desired_state
BEGIN
 SELECT RAISE(ABORT, 'workspace deletion is terminal');
END;
-- +goose StatementEnd
