-- +goose Up
CREATE TABLE origin_grants (
 id TEXT PRIMARY KEY,
 token_digest TEXT NOT NULL UNIQUE,
 user_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL,
 generation BIGINT NOT NULL CHECK (generation > 0),
 service_key TEXT NOT NULL,
 origin TEXT NOT NULL,
 permission_basis TEXT NOT NULL CHECK (permission_basis IN ('workspace.connect_owner','workspace.connect_admin')),
 resource_grant_id TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL,
 consumed_at TEXT,
 revoked_at TEXT,
 cookie_digest TEXT UNIQUE,
 cookie_expires_at TEXT,
 CHECK ((permission_basis = 'workspace.connect_admin') = (resource_grant_id <> '')),
 CHECK ((consumed_at IS NULL AND cookie_digest IS NULL AND cookie_expires_at IS NULL)
     OR (consumed_at IS NOT NULL AND cookie_digest IS NOT NULL AND cookie_expires_at IS NOT NULL))
);

-- +goose Down
DROP TABLE origin_grants;
