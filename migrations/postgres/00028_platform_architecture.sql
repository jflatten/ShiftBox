-- +goose Up
ALTER TABLE platform_artifacts DROP CONSTRAINT platform_artifacts_architecture_check;
ALTER TABLE platform_artifacts ADD CONSTRAINT platform_artifacts_architecture_check
 CHECK (architecture IN ('amd64','arm64'));
