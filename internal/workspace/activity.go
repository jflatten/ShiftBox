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
	cpuObserved := policy.CPU && cpu != nil && cpu.Percent >= 0 && cpu.Percent <= 100 &&
		!cpu.SampledAt.IsZero() && !cpu.SampledAt.After(received) && received.Sub(cpu.SampledAt) <= policy.Freshness
	cpuActive := cpuObserved && cpu.Percent > policy.CPUThreshold
	ideActive := policy.IDE && ideConnected
	if !cpuObserved && !policy.IDE {
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
	// Keep only the latest observation for each selected signal. Idle samples
	// remain useful evidence without accumulating history or moving deadlines.
	const observation = `INSERT INTO activity_observations(workspace_id,generation,source,active,received_at)
		SELECT id,generation,$3,$4,$5 FROM workspaces
		WHERE id=$1 AND generation=$2 AND desired_state='running' AND observed_state IN ('starting','running')
		AND deletion_commit_at IS NULL
		ON CONFLICT(workspace_id,generation,source) DO UPDATE SET active=excluded.active,received_at=excluded.received_at
		WHERE activity_observations.received_at < excluded.received_at`
	stamp := received.UTC().Format("2006-01-02T15:04:05.000000000Z")
	if cpuObserved {
		if _, err := db.ExecContext(ctx, observation, id, generation, "cpu", cpuActive, stamp); err != nil {
			return err
		}
	}
	if policy.IDE {
		if _, err := db.ExecContext(ctx, observation, id, generation, "ide", ideActive, stamp); err != nil {
			return err
		}
	}
	if !cpuActive && !ideActive {
		return nil
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
	deadline := RetentionDeadline(received, &retention.Int64)
	_, err = db.ExecContext(ctx, `UPDATE workspaces SET last_activity_at=$1,retention_deadline=$2
		WHERE id=$3 AND generation=$4 AND desired_state='running' AND observed_state IN ('starting','running')
		AND deletion_commit_at IS NULL AND COALESCE(last_activity_at,'')=$5
		AND COALESCE(retention_seconds,0)=$6`,
		received.UTC().Format(time.RFC3339Nano), deadline, id, generation, prior.String, retention.Int64)
	return err
}
