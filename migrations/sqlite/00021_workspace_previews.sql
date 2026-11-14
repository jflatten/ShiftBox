-- +goose Up
ALTER TABLE workspaces ADD COLUMN preview_ports_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE workspaces DROP COLUMN preview_ports_json;
