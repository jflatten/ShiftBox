-- +goose Up
ALTER TABLE workspaces ADD COLUMN network_policy_override_json TEXT;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN network_policy_override_json;
