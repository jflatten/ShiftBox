package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Probe result identifiers and states shared by the guest and controller.
const (
	ProbeCodeServer = "code-server"
	ProbeHealthy    = "healthy"
	ProbeError      = "error"

	probeFreshness  = 90 * time.Second
	probeTimeFormat = "2006-01-02T15:04:05.000000000Z"
)

// ProbeResult is the guest's observation of a platform service.
type ProbeResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ValidProbeResults checks the supported probe IDs and states at the wire boundary.
func ValidProbeResults(results []ProbeResult) bool {
	if len(results) == 0 {
		return true
	}
	return len(results) == 1 && results[0].ID == ProbeCodeServer &&
		(results[0].Status == ProbeHealthy || results[0].Status == ProbeError)
}

// RecordProbeResults stores the latest reported snapshot on the authenticated registration.
func RecordProbeResults(ctx context.Context, tx *sql.Tx, id string, generation int64, now time.Time, results []ProbeResult) error {
	if len(results) == 0 {
		return nil
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE guest_registrations SET probe_results_json=$3,probe_reported_at=$4
		WHERE workspace_id=$1 AND generation=$2 AND (probe_reported_at IS NULL OR probe_reported_at<=$4)`,
		id, generation, string(raw), now.UTC().Format(probeTimeFormat))
	return err
}

// ReadProbeResults returns recent observations for the current active guest registration.
func ReadProbeResults(ctx context.Context, tx *sql.Tx, id string, generation int64, now time.Time) ([]ProbeResult, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT probe_results_json FROM guest_registrations
		WHERE workspace_id=$1 AND generation=$2 AND state='active' AND probe_reported_at>$3`,
		id, generation, now.Add(-probeFreshness).UTC().Format(probeTimeFormat)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []ProbeResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	var results []ProbeResult
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return nil, err
	}
	return results, nil
}
