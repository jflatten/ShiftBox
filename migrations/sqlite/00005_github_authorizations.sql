-- +goose Up
CREATE TABLE github_authorizations (
 user_id TEXT NOT NULL REFERENCES users(id),
 app_id TEXT NOT NULL,
 provider_user_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('connected','refreshing','reconnect_required','revoked')),
 nonce BYTEA NOT NULL,
 ciphertext BYTEA NOT NULL,
 access_expires_at TEXT NOT NULL,
 refresh_expires_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0),
 PRIMARY KEY(user_id,app_id)
);

-- +goose Down
DROP TABLE github_authorizations;
