-- +goose Up
CREATE TABLE notices (
 id TEXT PRIMARY KEY,
 severity TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
 title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
 body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
 effective_at TEXT NOT NULL,
 expires_at TEXT CHECK (expires_at IS NULL OR expires_at > effective_at),
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL
);

CREATE TABLE notice_receipts (
 notice_id TEXT NOT NULL REFERENCES notices(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 read_at TEXT NOT NULL,
 dismissed_at TEXT,
 PRIMARY KEY (notice_id,user_id)
);

-- +goose Down
DROP TABLE notice_receipts;
DROP TABLE notices;
