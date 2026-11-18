-- +goose Up
ALTER TABLE installations ADD COLUMN session_timeout_value BIGINT NOT NULL DEFAULT 30 CHECK (session_timeout_value > 0);
ALTER TABLE installations ADD COLUMN session_timeout_unit TEXT NOT NULL DEFAULT 'minutes'
 CHECK (CASE session_timeout_unit
  WHEN 'minutes' THEN session_timeout_value <= 576000
  WHEN 'hours' THEN session_timeout_value <= 9600
  WHEN 'days' THEN session_timeout_value <= 400
  ELSE FALSE END);
ALTER TABLE installations ADD COLUMN session_timeout_version BIGINT NOT NULL DEFAULT 1 CHECK (session_timeout_version > 0);

-- +goose Down
ALTER TABLE installations DROP COLUMN session_timeout_version;
ALTER TABLE installations DROP COLUMN session_timeout_unit;
ALTER TABLE installations DROP COLUMN session_timeout_value;
