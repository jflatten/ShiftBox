-- +goose Up
ALTER TABLE guest_dispatches ADD COLUMN update_digest TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE guest_dispatches DROP COLUMN update_digest;
