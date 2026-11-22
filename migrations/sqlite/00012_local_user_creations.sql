-- +goose Up
CREATE TABLE local_user_creations (
 email TEXT PRIMARY KEY,
 user_id TEXT NOT NULL UNIQUE,
 role TEXT NOT NULL CHECK (role IN ('member','administrator')),
 credential_json TEXT NOT NULL
);

-- +goose Down
DROP TABLE local_user_creations;
