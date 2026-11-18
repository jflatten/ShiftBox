-- +goose Up
ALTER TABLE operation_steps ADD COLUMN transition_deadline TEXT;
ALTER TABLE operation_steps ADD COLUMN transition_timeout_at TEXT;
ALTER TABLE operation_steps ADD COLUMN transition_timeout_reason TEXT
 CHECK (transition_timeout_reason IS NULL OR
        (transition_timeout_at IS NOT NULL AND length(transition_timeout_reason) BETWEEN 1 AND 4096));

-- +goose Down
ALTER TABLE operation_steps DROP COLUMN transition_timeout_reason;
ALTER TABLE operation_steps DROP COLUMN transition_timeout_at;
ALTER TABLE operation_steps DROP COLUMN transition_deadline;
