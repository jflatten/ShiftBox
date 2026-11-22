-- +goose Up
-- Keep startup image evidence tied to the enrolled workspace generation.
ALTER TABLE workspaces ADD COLUMN image_cache_json TEXT NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN image_cache_generation BIGINT NOT NULL DEFAULT 0;
