-- +goose Up
-- Zero means unlimited for one budget dimension; preserve configured budgets.
CREATE TABLE capacity_budgets_unlimited (
 installation_id TEXT NOT NULL PRIMARY KEY REFERENCES installations(id),
 cpu_limit BIGINT NOT NULL CHECK (cpu_limit >= 0),
 memory_mib_limit BIGINT NOT NULL CHECK (memory_mib_limit >= 0),
 disk_mib_limit BIGINT NOT NULL CHECK (disk_mib_limit >= 0),
 updated_by TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0)
);

INSERT INTO capacity_budgets_unlimited
 (installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
SELECT installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version
FROM capacity_budgets;

DROP TABLE capacity_budgets;
ALTER TABLE capacity_budgets_unlimited RENAME TO capacity_budgets;
