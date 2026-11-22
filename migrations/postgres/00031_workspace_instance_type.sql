-- +goose Up
ALTER TABLE resource_profiles ADD COLUMN instance_type TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN instance_type TEXT NOT NULL DEFAULT '';

CREATE TRIGGER immutable_workspace_instance_type
BEFORE UPDATE OF instance_type ON workspaces
FOR EACH ROW EXECUTE FUNCTION reject_workspace_snapshot_update();
