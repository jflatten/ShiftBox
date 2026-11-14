-- +goose Up
CREATE TABLE platform_imports (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 installation_id TEXT NOT NULL REFERENCES installations(id),
 folder TEXT NOT NULL CHECK (length(folder) BETWEEN 1 AND 4096),
 make_default INTEGER NOT NULL CHECK (make_default IN (0,1))
);

-- +goose Down
DROP TABLE platform_imports;
