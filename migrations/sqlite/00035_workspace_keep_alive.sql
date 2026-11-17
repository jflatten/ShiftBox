-- +goose Up
-- Keep shutdown policy separate from destructive retention. Zero is Unlimited.
ALTER TABLE installations ADD COLUMN keep_alive_maximum_minutes BIGINT NOT NULL DEFAULT 0 CHECK (keep_alive_maximum_minutes BETWEEN 0 AND 153722867);
ALTER TABLE installations ADD COLUMN keep_alive_allowed_minutes_json TEXT NOT NULL DEFAULT '[30,60,120,240,480,720,0]';
ALTER TABLE installations ADD COLUMN keep_alive_default_minutes BIGINT NOT NULL DEFAULT 30 CHECK (keep_alive_default_minutes BETWEEN 0 AND 153722867);
ALTER TABLE installations ADD COLUMN keep_alive_version BIGINT NOT NULL DEFAULT 1 CHECK (keep_alive_version > 0);
ALTER TABLE workspaces ADD COLUMN keep_alive_minutes BIGINT CHECK (keep_alive_minutes BETWEEN 0 AND 153722867);
