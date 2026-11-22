-- +goose Up
CREATE TABLE guest_configurations (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 dispatch_version BIGINT NOT NULL CHECK (dispatch_version > 0),
 status TEXT NOT NULL CHECK (status IN ('pending','running','succeeded','failed')),
 container_id TEXT NOT NULL DEFAULT '',
 problem TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE guest_configurations;
