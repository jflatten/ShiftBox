-- +goose Up
ALTER TABLE github_authorizations ADD COLUMN provider TEXT NOT NULL DEFAULT 'github.com' CHECK (provider = 'github.com');
ALTER TABLE github_authorizations ADD COLUMN refresh_generation BIGINT NOT NULL DEFAULT 0 CHECK (refresh_generation >= 0);
-- Existing refreshes keep the fence already assigned by the previous store.
UPDATE github_authorizations SET refresh_generation=version WHERE state='refreshing';
