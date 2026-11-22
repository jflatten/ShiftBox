-- +goose Up
ALTER TABLE guest_registrations ADD COLUMN probe_results_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE guest_registrations ADD COLUMN probe_reported_at TEXT;

-- +goose Down
ALTER TABLE guest_registrations DROP COLUMN probe_reported_at;
ALTER TABLE guest_registrations DROP COLUMN probe_results_json;
