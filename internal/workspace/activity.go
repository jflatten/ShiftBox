package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ActivityPolicy describes which observations can extend a workspace deadline.
type ActivityPolicy struct {
	IDE, CPU     bool
	CPUThreshold float64
	Freshness    time.Duration
}

// CPUActivity is advisory telemetry from an authenticated guest.
type CPUActivity struct {
	SampledAt time.Time `json:"sampledAt"`
	Percent   float64   `json:"cpuPercent"`
}

// RecordActivity advances retention using controller receipt time. It ignores
// stopped workspaces, stale generations and observations after deletion commits.
func RecordActivity(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id string, generation int64, received time.Time, policy ActivityPolicy, cpu *CPUActivity, ideConnected bool) error {
	cpuActive := policy.CPU && cpu != nil && cpu.Percent > policy.CPUThreshold && cpu.Percent <= 100 &&
		!cpu.SampledAt.IsZero() && !cpu.SampledAt.After(received) && received.Sub(cpu.SampledAt) <= policy.Freshness
	if !cpuActive && !(policy.IDE && ideConnected) {
		return nil
	}
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
	var deadline *string
	if retention.Valid && retention.Int64 > 0 && retention.Int64 <= int64((1<<63-1)/time.Second) {
		value := received.UTC().Add(time.Duration(retention.Int64) * time.Second).Format(time.RFC3339Nano)
		deadline = &value
	}
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET last_activity_at=$1,retention_deadline=$2
		WHERE id=$3 AND generation=$4 AND desired_state='running' AND observed_state IN ('starting','running')
		AND deletion_commit_at IS NULL AND COALESCE(last_activity_at,'')=$5
		AND COALESCE(retention_seconds,0)=$6`,
		received.UTC().Format(time.RFC3339Nano), deadline, id, generation, prior.String, retention.Int64)
	return err
}
