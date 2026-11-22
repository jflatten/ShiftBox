-- +goose Up
CREATE TABLE github_oauth_states (
 user_id TEXT NOT NULL REFERENCES users(id),
 app_id TEXT NOT NULL,
 digest TEXT NOT NULL UNIQUE,
 redirect_uri TEXT NOT NULL,
 nonce BYTEA NOT NULL,
 ciphertext BYTEA NOT NULL,
 expires_at TEXT NOT NULL,
 used INTEGER NOT NULL DEFAULT 0 CHECK (used IN (0,1)),
 PRIMARY KEY(user_id,app_id)
);

-- +goose Down
DROP TABLE github_oauth_states;
