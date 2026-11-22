-- +goose Up
ALTER TABLE guest_dispatches ADD COLUMN activation_status TEXT NOT NULL DEFAULT '' CHECK (activation_status IN ('','succeeded','failed'));

-- +goose Down
ALTER TABLE guest_dispatches DROP COLUMN activation_status;
