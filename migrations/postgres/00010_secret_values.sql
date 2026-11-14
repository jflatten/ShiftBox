-- +goose Up
CREATE TABLE secret_values (
 id TEXT NOT NULL PRIMARY KEY,
 scope_type TEXT NOT NULL CHECK (scope_type IN ('user','template','organization')),
 scope_id TEXT NOT NULL CHECK (length(scope_id) > 0),
 name TEXT NOT NULL CHECK (name ~ '^[A-Z][A-Z0-9_]{0,126}$'),
 ciphertext_json TEXT NOT NULL,
 key_version INTEGER NOT NULL CHECK (key_version > 0),
 is_explicit_empty INTEGER NOT NULL CHECK (is_explicit_empty IN (0,1)),
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0),
 UNIQUE (scope_type,scope_id,name)
);

-- +goose Down
DROP TABLE secret_values;
