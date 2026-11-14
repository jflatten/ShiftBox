package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// RetentionDeadline derives a deadline from a controller-owned clock.
// An absent duration leaves automatic retention disabled.
func RetentionDeadline(baseline time.Time, seconds *int64) *string {
	if baseline.IsZero() || seconds == nil || *seconds <= 0 || *seconds > int64((1<<63-1)/time.Second) {
		return nil
	}
	deadline := baseline.UTC().Add(time.Duration(*seconds) * time.Second).Format(time.RFC3339Nano)
	return &deadline
}

// RetentionEligibility describes the current administrator limit and provider
// recovery window. Reconciliation supplies a new timestamp after an outage.
type RetentionEligibility struct {
	MaximumSeconds *int64
	ReconciledAt   time.Time
	OutageGrace    time.Duration
}

func (p RetentionEligibility) ready(now time.Time) bool {
	return p.MaximumSeconds != nil && RetentionDeadline(now, p.MaximumSeconds) != nil &&
		!p.ReconciledAt.IsZero() && p.OutageGrace > 0 && !now.Before(p.ReconciledAt.Add(p.OutageGrace))
}

// RetentionCandidate identifies an overdue workspace with matching provider
// evidence. Discovery never grants permission to delete it.
type RetentionCandidate struct {
	Ref      provider.WorkspaceRef
	Deadline time.Time
	request  provider.CreateRequest
	desired  string
	observed string
	version  int64
}

const retentionProjection = `SELECT w.installation_id,w.id,w.generation,w.provider_instance_id,w.ownership_marker,
	w.desired_state,w.observed_state,w.version,w.platform_artifact_id,a.release_id,
	COALESCE(w.network_policy_override_json,w.network_policy_snapshot_json),
	w.retention_seconds,COALESCE(w.last_activity_at,w.created_at)
	FROM workspaces w JOIN installations i ON i.id=w.installation_id
	JOIN platform_artifacts a ON a.id=w.platform_artifact_id JOIN users u ON u.id=w.owner_user_id
	WHERE w.installation_id=$1 AND i.state='active' AND u.membership_state='active'
	AND w.retention_seconds BETWEEN 1 AND $2 AND w.deletion_commit_at IS NULL
	AND w.desired_state IN ('stopped','running') AND w.observed_state IN ('stopped','running','unreachable')
	AND w.provider_instance_id IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		AND o.status IN ('pending','running','waiting'))`

// RetentionCandidates inspects overdue workspaces once grace has elapsed and
// returns only those that still match durable authority and provider identity.
func (s *Store) RetentionCandidates(ctx context.Context, installation string, now time.Time, policy RetentionEligibility,
	inspect func(context.Context, provider.WorkspaceRef) (provider.Inventory, error),
) ([]RetentionCandidate, error) {
	if !policy.ready(now) {
		return nil, nil
	}
	pending, err := s.dueRetention(ctx, installation, now, *policy.MaximumSeconds)
	if err != nil {
		return nil, err
	}
	var verified []RetentionCandidate
	for _, candidate := range pending {
		inventory, err := inspect(ctx, candidate.Ref)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || verifyWorkspaceVM(candidate.request, inventory) != nil ||
			(inventory.Power != provider.Stopped && inventory.Power != provider.Running) {
			continue
		}
		current, err := recheckRetention(ctx, s.db, candidate, now, policy)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		verified = append(verified, current)
	}
	return verified, nil
}

func (s *Store) dueRetention(ctx context.Context, installation string, now time.Time, maximum int64) ([]RetentionCandidate, error) {
	rows, err := s.db.QueryContext(ctx, retentionProjection+" ORDER BY w.id", installation, maximum)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []RetentionCandidate
	for rows.Next() {
		candidate, err := scanRetention(rows)
		if err != nil {
			return nil, err
		}
		if !now.Before(candidate.Deadline) {
			pending = append(pending, candidate)
		}
	}
	return pending, rows.Err()
}

func recheckRetention(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, candidate RetentionCandidate, now time.Time, policy RetentionEligibility) (RetentionCandidate, error) {
	if !policy.ready(now) {
		return RetentionCandidate{}, sql.ErrNoRows
	}
	current, err := scanRetention(db.QueryRowContext(ctx, retentionProjection+" AND w.id=$3",
		candidate.Ref.InstallationID, *policy.MaximumSeconds, candidate.Ref.WorkspaceID))
	if err != nil {
		return RetentionCandidate{}, err
	}
	if current.request != candidate.request || current.version != candidate.version ||
		current.desired != candidate.desired || current.observed != candidate.observed ||
		!current.Deadline.Equal(candidate.Deadline) || now.Before(current.Deadline) {
		return RetentionCandidate{}, sql.ErrNoRows
	}
	return current, nil
}

func scanRetention(row interface{ Scan(...any) error }) (RetentionCandidate, error) {
	var candidate RetentionCandidate
	var baseline, network string
	var duration int64
	err := row.Scan(&candidate.Ref.InstallationID, &candidate.Ref.WorkspaceID, &candidate.Ref.Generation,
		&candidate.Ref.InstanceID, &candidate.Ref.OwnershipMarker, &candidate.desired, &candidate.observed, &candidate.version,
		&candidate.request.ArtifactID, &candidate.request.Release, &network, &duration, &baseline)
	if err != nil {
		return RetentionCandidate{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, baseline)
	if err != nil {
		return RetentionCandidate{}, err
	}
	candidate.Deadline = at.Add(time.Duration(duration) * time.Second)
	var snapshot struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal([]byte(network), &snapshot) != nil || snapshot.Mode == "" {
		return RetentionCandidate{}, provider.ErrInvalidState
	}
	candidate.request.Ref, candidate.request.NetworkMode = candidate.Ref, snapshot.Mode
	return candidate, nil
}
