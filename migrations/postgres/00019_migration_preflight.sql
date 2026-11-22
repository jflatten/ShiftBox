-- +goose Up
CREATE TABLE migration_preflights (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 installation_id TEXT NOT NULL REFERENCES installations(id),
 release_id TEXT NOT NULL CHECK (length(release_id) BETWEEN 1 AND 128)
);

-- +goose Down
DROP TABLE migration_preflights;
