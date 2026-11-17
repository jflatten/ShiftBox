-- +goose Up
ALTER TABLE resource_profiles ADD COLUMN instance_type TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN instance_type TEXT NOT NULL DEFAULT '';

-- +goose StatementBegin
CREATE TRIGGER immutable_workspace_instance_type
BEFORE UPDATE OF instance_type ON workspaces
BEGIN
 SELECT RAISE(ABORT, 'workspace instance type is immutable');
END;
-- +goose StatementEnd
