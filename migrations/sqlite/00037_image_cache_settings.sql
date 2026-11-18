-- +goose Up
ALTER TABLE installations ADD COLUMN image_cache_settings_json TEXT NOT NULL DEFAULT '';
ALTER TABLE installations ADD COLUMN image_cache_version INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE installations DROP COLUMN image_cache_version;
ALTER TABLE installations DROP COLUMN image_cache_settings_json;
