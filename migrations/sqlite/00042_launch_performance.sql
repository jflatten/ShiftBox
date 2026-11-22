-- +goose Up
ALTER TABLE installations ADD COLUMN launch_log_retention_days INTEGER NOT NULL DEFAULT 30
 CHECK (launch_log_retention_days BETWEEN 1 AND 3650);
ALTER TABLE installations ADD COLUMN launch_log_retention_version BIGINT NOT NULL DEFAULT 1
 CHECK (launch_log_retention_version > 0);

CREATE TABLE launch_timing_events (
 operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
 source TEXT NOT NULL CHECK (source IN ('provider','guest','controller')),
 id TEXT NOT NULL,
 phase TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK (outcome IN ('succeeded','failed','observed')),
 occurred_at TEXT NOT NULL,
 duration_ms BIGINT NOT NULL CHECK (duration_ms >= 0),
 reason_code TEXT NOT NULL DEFAULT '',
 PRIMARY KEY (operation_id,source,id)
);

CREATE TABLE launch_cache_stats (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
 requests INTEGER NOT NULL CHECK (requests > 0),
 cached_requests INTEGER NOT NULL CHECK (cached_requests >= 0),
 failed_requests INTEGER NOT NULL CHECK (failed_requests >= 0),
 rate_limited_requests INTEGER NOT NULL CHECK (rate_limited_requests >= 0),
 cached_bytes BIGINT NOT NULL CHECK (cached_bytes >= 0),
 upstream_bytes BIGINT NOT NULL CHECK (upstream_bytes >= 0),
 first_request_at TEXT NOT NULL,
 last_response_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE launch_cache_stats;
DROP TABLE launch_timing_events;
ALTER TABLE installations DROP COLUMN launch_log_retention_version;
ALTER TABLE installations DROP COLUMN launch_log_retention_days;
