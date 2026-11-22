-- +goose Up
CREATE TABLE helper_credentials (
 credential_digest TEXT PRIMARY KEY NOT NULL CHECK (length(credential_digest) = 64 AND credential_digest NOT GLOB '*[^0-9a-f]*'),
 approving_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX helper_credentials_by_session ON helper_credentials(approving_session_id);

-- +goose Down
DROP TABLE helper_credentials;
