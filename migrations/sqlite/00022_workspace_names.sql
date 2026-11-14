-- +goose Up
ALTER TABLE workspaces ADD COLUMN name TEXT NOT NULL DEFAULT '' CHECK (length(name) <= 120);

-- +goose Down
ALTER TABLE workspaces DROP COLUMN name;
