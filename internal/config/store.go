package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ErrActivationConflict means the reviewed configuration generation is no longer current.
var ErrActivationConflict = errors.New("configuration activation conflict")

// Activation records already-validated, non-secret settings and protected-value references.
// Certificate, profile and hostname validation belongs to the consuming activation flow.
type Activation struct {
	InstallationID     string
	ExpectedGeneration int64
	SettingsJSON       string
	ActorID            string
	ActivatedAt        time.Time
}

// RecordActivation advances the generation and retires its predecessor in the
// caller's transaction. Any error requires rollback; external effects happen only
// after the full activation transaction commits successfully.
func RecordActivation(ctx context.Context, tx *sql.Tx, activation Activation) (int64, error) {
	var settings map[string]json.RawMessage
	if activation.InstallationID == "" || activation.ActorID == "" || activation.ActivatedAt.IsZero() || activation.ExpectedGeneration < 0 || activation.ExpectedGeneration == 1<<63-1 || json.Unmarshal([]byte(activation.SettingsJSON), &settings) != nil || settings == nil {
		return 0, errors.New("invalid configuration activation record")
	}
	next := activation.ExpectedGeneration + 1
	stamp := activation.ActivatedAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE installations SET config_generation=$1,updated_at=$2,version=version+1 WHERE id=$3 AND config_generation=$4`, next, stamp, activation.InstallationID, activation.ExpectedGeneration)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrActivationConflict
	}
	if activation.ExpectedGeneration > 0 {
		result, err := tx.ExecContext(ctx, `UPDATE activated_configurations SET retired_at=$1 WHERE installation_id=$2 AND generation=$3 AND retired_at IS NULL`, stamp, activation.InstallationID, activation.ExpectedGeneration)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if count != 1 {
			return 0, ErrActivationConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at) VALUES($1,$2,$3,$4,$5)`, activation.InstallationID, next, activation.SettingsJSON, activation.ActorID, stamp)
	if err != nil {
		return 0, err
	}
	return next, nil
}
