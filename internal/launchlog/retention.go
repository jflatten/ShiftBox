package launchlog

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// Retention is the versioned installation policy for launch diagnostics.
type Retention struct {
	Days    int   `json:"days"`
	Version int64 `json:"version"`
}

// ValidRetentionDays checks the supported day range before arithmetic or writes.
func ValidRetentionDays(days int) bool { return days >= 1 && days <= 3650 }

// ReadRetention returns the installation policy.
func ReadRetention(ctx context.Context, db reader) (Retention, error) {
	var settings Retention
	err := db.QueryRowContext(ctx, `SELECT launch_log_retention_days,launch_log_retention_version
		FROM installations WHERE singleton=1`).Scan(&settings.Days, &settings.Version)
	return settings, err
}

// Prune expires supplemental diagnostics for completed launches. The cutoff
// and deletes share a transaction with a lock on the policy being enforced.
func Prune(ctx context.Context, db *sql.DB, now time.Time) error {
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var days int
		err := tx.QueryRowContext(ctx, `UPDATE installations SET launch_log_retention_version=launch_log_retention_version
			WHERE singleton=1 AND state='active' RETURNING launch_log_retention_days`).Scan(&days)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		cutoff := stamp(now.UTC().AddDate(0, 0, -days))
		if _, err := tx.ExecContext(ctx, `DELETE FROM launch_timing_events WHERE operation_id IN
			(SELECT id FROM operations WHERE status IN ('succeeded','failed','cancelled') AND finished_at<$1)`, cutoff); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM launch_cache_stats WHERE operation_id IN
			(SELECT id FROM operations WHERE status IN ('succeeded','failed','cancelled') AND finished_at<$1)`, cutoff)
		return err
	})
}
