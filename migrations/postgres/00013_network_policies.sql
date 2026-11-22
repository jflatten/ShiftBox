-- +goose Up
CREATE TABLE network_policies (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL UNIQUE CHECK (length(trim(name)) BETWEEN 1 AND 120),
 mode TEXT NOT NULL CHECK (mode IN ('all','no_network','allowlist')),
 rules_json TEXT NOT NULL,
 required_exceptions_json TEXT NOT NULL,
 provider_capability_version TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
 version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0)
);

-- +goose Down
DROP TABLE network_policies;
