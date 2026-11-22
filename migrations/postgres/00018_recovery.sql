-- +goose Up
CREATE TABLE backup_records (
 id TEXT PRIMARY KEY,
 operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 format_version BIGINT NOT NULL CHECK (format_version > 0),
 product_release TEXT NOT NULL CHECK (length(product_release) > 0),
 schema_versions_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('preparing','checkpointing','encrypting','completed','failed')),
 manifest_digest TEXT CHECK (length(manifest_digest) = 64 AND manifest_digest ~ '^[0-9a-f]+$'),
 archive_digest TEXT CHECK (length(archive_digest) = 64 AND archive_digest ~ '^[0-9a-f]+$'),
 size_bytes BIGINT CHECK (size_bytes > 0),
 created_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 completed_at TEXT,
 error_code TEXT,
 CHECK (state <> 'completed' OR (manifest_digest IS NOT NULL AND archive_digest IS NOT NULL
   AND size_bytes IS NOT NULL AND completed_at IS NOT NULL AND error_code IS NULL)),
 CHECK (state = 'completed' OR completed_at IS NULL)
);

CREATE TABLE recovery_reviews (
 id TEXT PRIMARY KEY,
 restore_operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id),
 state TEXT NOT NULL CHECK (state IN ('identity_review','provider_reconcile','authorization_review','ready','activated','blocked')),
 reviewed_users_json TEXT NOT NULL DEFAULT '[]',
 reconciled_workspaces_json TEXT NOT NULL DEFAULT '[]',
 started_by TEXT NOT NULL,
 started_at TEXT NOT NULL,
 approved_by TEXT,
 activated_at TEXT,
 version BIGINT NOT NULL CHECK (version > 0),
 CHECK ((state = 'activated' AND approved_by IS NOT NULL AND activated_at IS NOT NULL)
   OR (state <> 'activated' AND approved_by IS NULL AND activated_at IS NULL))
);

-- +goose Down
DROP TABLE recovery_reviews;
DROP TABLE backup_records;
