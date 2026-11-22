-- +goose Up
ALTER TABLE installations ADD COLUMN built_image_enabled INTEGER NOT NULL DEFAULT 1
 CHECK (built_image_enabled IN (0,1));
ALTER TABLE installations ADD COLUMN built_image_version BIGINT NOT NULL DEFAULT 1
 CHECK (built_image_version > 0);
ALTER TABLE installations ADD COLUMN built_image_generation BIGINT NOT NULL DEFAULT 1
 CHECK (built_image_generation > 0);
ALTER TABLE installations ADD COLUMN built_image_purge_cutoff BIGINT
 CHECK (built_image_purge_cutoff > 0 AND built_image_purge_cutoff < built_image_generation);

-- The pointer names the immutable staged namespace within a managed storage root.
CREATE TABLE built_image_slots (
 installation_id TEXT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES project_templates(id) ON DELETE CASCADE,
 target_os TEXT NOT NULL CHECK (target_os <> ''),
 target_arch TEXT NOT NULL CHECK (target_arch <> ''),
 repository_url TEXT NOT NULL,
 config_path TEXT NOT NULL,
 config_sha256 TEXT NOT NULL CHECK (length(config_sha256) = 64),
 manifest_digest TEXT NOT NULL CHECK (length(manifest_digest) = 71 AND substr(manifest_digest,1,7) = 'sha256:'),
 policy_generation BIGINT NOT NULL CHECK (policy_generation > 0),
 storage_generation BIGINT NOT NULL CHECK (storage_generation > 0),
 storage_path TEXT NOT NULL CHECK (storage_path <> ''),
 storage_scope TEXT NOT NULL CHECK (storage_scope <> ''),
 publication_kind TEXT NOT NULL CHECK (publication_kind IN ('launch','rebuild')),
 origin_revision TEXT NOT NULL CHECK (origin_revision <> ''),
 promoted_at TEXT NOT NULL,
 PRIMARY KEY (installation_id,project_id,target_os,target_arch)
);

-- Attempts preserve the authority seen at admission until completion or expiry.
CREATE TABLE built_image_attempts (
 id TEXT PRIMARY KEY NOT NULL,
 installation_id TEXT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES project_templates(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 workspace_generation BIGINT NOT NULL CHECK (workspace_generation > 0),
 operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
 dispatch_version BIGINT NOT NULL CHECK (dispatch_version > 0),
 target_os TEXT NOT NULL CHECK (target_os <> ''),
 target_arch TEXT NOT NULL CHECK (target_arch <> ''),
 repository_url TEXT NOT NULL,
 config_path TEXT NOT NULL,
 config_sha256 TEXT NOT NULL CHECK (length(config_sha256) = 64),
 policy_generation BIGINT NOT NULL CHECK (policy_generation > 0),
 source_authorization_version BIGINT NOT NULL CHECK (source_authorization_version > 0),
 kind TEXT NOT NULL CHECK (kind IN ('launch','rebuild')),
 state TEXT NOT NULL CHECK (state IN ('admitted','receiving','complete','promoted','discarded','expired')),
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL CHECK (expires_at > created_at),
 manifest_digest TEXT CHECK (length(manifest_digest) = 71 AND substr(manifest_digest,1,7) = 'sha256:'),
 storage_path TEXT NOT NULL CHECK (storage_path <> ''),
 storage_generation BIGINT NOT NULL CHECK (storage_generation > 0),
 CHECK (state NOT IN ('complete','promoted') OR manifest_digest IS NOT NULL)
);
CREATE INDEX built_image_attempts_expiry ON built_image_attempts(installation_id,expires_at);
CREATE INDEX built_image_attempts_operation ON built_image_attempts(operation_id,dispatch_version);

-- +goose Down
DROP TABLE built_image_attempts;
DROP TABLE built_image_slots;
ALTER TABLE installations DROP COLUMN built_image_purge_cutoff;
ALTER TABLE installations DROP COLUMN built_image_generation;
ALTER TABLE installations DROP COLUMN built_image_version;
ALTER TABLE installations DROP COLUMN built_image_enabled;
