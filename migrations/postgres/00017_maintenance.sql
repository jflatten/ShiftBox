-- +goose Up
CREATE TABLE maintenance_campaigns (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 installation_id TEXT NOT NULL REFERENCES installations(id),
 release_id TEXT NOT NULL CHECK (length(release_id) > 0),
 deadline TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('scheduled','active','blocked','completed','cancelled'))
);

CREATE TABLE maintenance_targets (
 campaign_id TEXT NOT NULL REFERENCES maintenance_campaigns(operation_id),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 generation BIGINT NOT NULL CHECK (generation > 0),
 prior_desired_state TEXT NOT NULL,
 prior_observed_state TEXT NOT NULL,
 operation_id TEXT UNIQUE REFERENCES operations(id),
 state TEXT NOT NULL CHECK (state IN ('pending','waiting_safe','updating','verified','restoring_state','failed','completed')),
 problem TEXT,
 updated_at TEXT NOT NULL,
 PRIMARY KEY (campaign_id,workspace_id)
);

-- +goose Down
DROP TABLE maintenance_targets;
DROP TABLE maintenance_campaigns;
