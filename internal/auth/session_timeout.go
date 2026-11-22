package auth

import (
	"context"
	"database/sql"
	"time"
)

// Browsers can cap the expiration of a persistent cookie at 400 days.
const maxSessionLifetime = 400 * 24 * time.Hour

// SessionTimeout is the installation's policy for newly issued management sessions.
type SessionTimeout struct {
	Value   int64  `json:"value"`
	Unit    string `json:"unit"`
	Version int64  `json:"version"`
}

// Duration converts a valid policy to the lifetime of a session cookie.
func (s SessionTimeout) Duration() (time.Duration, bool) {
	var unit time.Duration
	switch s.Unit {
	case "minutes":
		unit = time.Minute
	case "hours":
		unit = time.Hour
	case "days":
		unit = 24 * time.Hour
	default:
		return 0, false
	}
	if s.Value <= 0 || s.Value > int64(maxSessionLifetime/unit) {
		return 0, false
	}
	return time.Duration(s.Value) * unit, true
}

// ReadSessionTimeout loads the policy alongside the authority for the transaction.
func ReadSessionTimeout(ctx context.Context, tx *sql.Tx, installationID string) (SessionTimeout, error) {
	var timeout SessionTimeout
	err := tx.QueryRowContext(ctx, `SELECT session_timeout_value,session_timeout_unit,session_timeout_version
		FROM installations WHERE id=$1`, installationID).Scan(&timeout.Value, &timeout.Unit, &timeout.Version)
	return timeout, err
}
