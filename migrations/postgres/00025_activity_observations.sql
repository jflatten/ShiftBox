-- +goose Up
CREATE TABLE activity_observations (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 generation BIGINT NOT NULL CHECK (generation > 0),
 source TEXT NOT NULL CHECK (source IN ('cpu','ide')),
 active BOOLEAN NOT NULL CHECK (active IN (FALSE,TRUE)),
 received_at TEXT NOT NULL,
 PRIMARY KEY (workspace_id,generation,source)
);
