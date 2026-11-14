-- +goose Up
CREATE TABLE operational_conditions (
 condition_key TEXT PRIMARY KEY,
 severity TEXT NOT NULL CHECK (severity IN ('warning','critical')),
 detail TEXT NOT NULL,
 observed_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE operational_conditions;
