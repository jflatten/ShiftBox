-- +goose Up
CREATE TABLE external_connector (
 installation_id TEXT PRIMARY KEY REFERENCES installations(id),
 issuer TEXT NOT NULL CHECK (length(issuer) > 0),
 connector_id TEXT NOT NULL CHECK (length(connector_id) > 0),
 dex_id TEXT NOT NULL UNIQUE CHECK (length(dex_id) > 0)
);

-- +goose Down
DROP TABLE external_connector;
