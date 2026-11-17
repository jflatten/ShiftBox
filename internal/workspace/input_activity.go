package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ProcessActivity is a fresh, advisory report of an explicit guest process watch.
type ProcessActivity struct {
	SampledAt time.Time `json:"sampledAt"`
	Active    bool      `json:"active"`
}

type activityDatabase interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// RecordHumanActivity advances the controller clock after an authenticated IDE
// input notification. The request supplies neither a workspace nor a timestamp.
func RecordHumanActivity(ctx context.Context, db activityDatabase, id string, generation int64, received time.Time) error {
	var prior sql.NullString
	var retention sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT last_activity_at,retention_seconds FROM workspaces
		WHERE id=$1 AND generation=$2 AND desired_state='running' AND observed_state IN ('starting','running')
		AND deletion_commit_at IS NULL`, id, generation).Scan(&prior, &retention)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if prior.Valid {
		last, err := time.Parse(time.RFC3339Nano, prior.String)
		if err != nil {
			return err
		}
		if !received.After(last) {
			return nil
		}
	}
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET last_activity_at=$1,retention_deadline=$2
		WHERE id=$3 AND generation=$4 AND desired_state='running' AND observed_state IN ('starting','running')
		AND deletion_commit_at IS NULL AND COALESCE(last_activity_at,'')=$5
		AND COALESCE(retention_seconds,0)=$6`,
		received.UTC().Format(time.RFC3339Nano), RetentionDeadline(received, &retention.Int64), id, generation, prior.String, retention.Int64)
	return err
}

// RecordProcessActivity renews the clock only while a fresh explicit watch is
// active. Missing reports, background CPU and open connections are not activity.
func RecordProcessActivity(ctx context.Context, db activityDatabase, id string, generation int64, received time.Time, report *ProcessActivity) error {
	if report == nil || !report.Active || report.SampledAt.IsZero() || report.SampledAt.After(received) || received.Sub(report.SampledAt) > 90*time.Second {
		return nil
	}
	return RecordHumanActivity(ctx, db, id, generation, received)
}
