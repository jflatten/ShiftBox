// Package launchlog records bounded diagnostics for admitted workspace launches and starts.
package launchlog

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
)

const (
	launchAction     = "workspace.launch"
	startAction      = "workspace.start"
	startupOperation = `((o.type='provision' AND o.originating_action='` + launchAction + `')
		OR (o.type='start' AND o.originating_action='` + startAction + `'))`
	// GuestPath is served on the authenticated guest HTTPS listener.
	GuestPath = "/internal/v1/guest/launch-timing"
)

// TracksOperation reports whether the workflow records launch performance.
func TracksOperation(kind, action string) bool {
	return kind == "provision" && action == launchAction || kind == "start" && action == startAction
}

var (
	// ErrGuestScope identifies a sample outside the guest's approved launch dispatch.
	ErrGuestScope = errors.New("launch timing does not match the guest dispatch")
	// ErrInvalidSample identifies a sample outside the supported phase and value set.
	ErrInvalidSample = errors.New("invalid launch timing sample")
)

// GuestSample carries an allowlisted measurement without command output or arguments.
type GuestSample struct {
	ID         string `json:"id"`
	Phase      string `json:"phase"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"durationMs"`
}

func (s GuestSample) valid() bool {
	id, err := uuid.Parse(s.ID)
	if err != nil || id == uuid.Nil || id.String() != s.ID || s.DurationMS < 0 || s.DurationMS > (24*time.Hour).Milliseconds() {
		return false
	}
	switch s.Phase {
	case "docker_image_pull", "docker_image_build", "docker_container_run", "docker_container_create", "docker_container_start", "docker_container_ready":
		return s.Outcome == "observed"
	case "repository_preparation", "image_prepare", "image_configuration", "image_prefetch", "devcontainer_up",
		"initial_devcontainer_setup", "recreate_with_retained_home", "retained_home", "project_permissions",
		"required_hooks", "remaining_hooks", "ide_mount", "ide_install", "ide_settings", "ide_extensions", "ide_start":
		return s.Outcome == "succeeded" || s.Outcome == "failed"
	default:
		return false
	}
}

type reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type writer interface {
	reader
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// RecordGuest adds one sample within the guest authentication transaction.
// The registration row locked by authentication serializes the cap and retries.
func RecordGuest(ctx context.Context, db writer, workspaceID string, generation int64, operationID string,
	dispatchVersion int64, sample GuestSample, now time.Time,
) error {
	if !sample.valid() {
		return ErrInvalidSample
	}
	var eligible bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM operations o JOIN guest_dispatches g ON g.operation_id=o.id
		WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4
		  AND `+startupOperation+`
		  AND o.status IN ('running','waiting','succeeded','failed')
		  AND (o.finished_at IS NULL OR o.finished_at >= $5))`,
		operationID, workspaceID, generation, dispatchVersion, stamp(now.Add(-10*time.Minute))).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrGuestScope
	}
	return insertTiming(ctx, db, operationID, "guest", sample.ID, sample.Phase, sample.Outcome, now, sample.DurationMS, "")
}

// RecordProvider stores a completed provider call with a stable, redacted error category.
func RecordProvider(ctx context.Context, db writer, operationID, phase string, started, finished time.Time, effectErr error) error {
	outcome := "succeeded"
	if effectErr != nil {
		outcome = "failed"
	}
	return insertTiming(ctx, db, operationID, "provider", uuid.NewString(), phase, outcome, finished,
		max(0, finished.Sub(started).Milliseconds()), providerReason(effectErr))
}

func providerReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, provider.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, provider.ErrOutcomeUnknown):
		return "outcome_unknown"
	case errors.Is(err, provider.ErrNotFound):
		return "resource_missing"
	case errors.Is(err, provider.ErrOwnershipMismatch):
		return "ownership_mismatch"
	case errors.Is(err, provider.ErrPermission):
		return "permission_denied"
	case errors.Is(err, provider.ErrCapabilityUnsupported):
		return "unsupported"
	case errors.Is(err, provider.ErrInvalidState):
		return "invalid_state"
	case errors.Is(err, provider.ErrPartial):
		return "partially_completed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "failed"
	}
}

// MarkVMRunning records the first verified provider-running observation, even after reconciliation.
func MarkVMRunning(ctx context.Context, db writer, operationID string, observedAt time.Time) error {
	return insertTiming(ctx, db, operationID, "provider", "vm_running", "vm_running", "observed", observedAt, 0, "")
}

// MarkConfigureDispatched records the first dispatch of launch configuration.
func MarkConfigureDispatched(ctx context.Context, tx *sql.Tx, operationID string, now time.Time) error {
	return insertTiming(ctx, tx, operationID, "controller", "configure_dispatched", "configure_dispatched", "observed", now, 0, "")
}

// MarkConfigureReported records the controller accepting the guest's waitFor result.
func MarkConfigureReported(ctx context.Context, tx *sql.Tx, operationID string, now time.Time) error {
	return insertTiming(ctx, tx, operationID, "controller", "configure_reported", "configure_reported", "observed", now, 0, "")
}

// MarkGuestAuthenticated anchors the first accepted poll to the startup in controller time.
func MarkGuestAuthenticated(ctx context.Context, tx *sql.Tx, workspaceID string, generation int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO launch_timing_events (operation_id,source,id,phase,outcome,occurred_at,duration_ms)
		SELECT o.id,'controller','guest_authenticated','guest_authenticated','observed',$1,0 FROM operations o
		WHERE o.workspace_id=$2 AND o.generation=$3 AND `+startupOperation+`
		  AND status IN ('running','waiting')
		ON CONFLICT(operation_id,source,id) DO NOTHING`, stamp(now), workspaceID, generation)
	return err
}

func insertTiming(ctx context.Context, db writer, operationID, source, id, phase, outcome string, at time.Time, elapsedMS int64, reason string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO launch_timing_events
		(operation_id,source,id,phase,outcome,occurred_at,duration_ms,reason_code)
		SELECT o.id,$2,$3,$4,$5,$6,$7,$8 FROM operations o
		WHERE o.id=$1 AND `+startupOperation+` AND
		  (SELECT COUNT(*) FROM launch_timing_events WHERE operation_id=$1 AND source=$2)<128
		ON CONFLICT(operation_id,source,id) DO NOTHING`,
		operationID, source, id, phase, outcome, stamp(at), elapsedMS, reason)
	return err
}

// ActiveCacheLaunch binds a registry request to the launch or start that dispatched
// setup. The request retains that association while its image layer streams.
func ActiveCacheLaunch(ctx context.Context, db reader, workspaceID string, generation int64) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT o.id FROM operations o JOIN guest_dispatches g ON g.operation_id=o.id
		WHERE o.workspace_id=$1 AND o.generation=$2 AND `+startupOperation+`
		  AND o.status IN ('running','waiting') AND o.current_step IN ('configure','verify_ide','finish_setup')
		ORDER BY o.created_at DESC,o.id DESC LIMIT 1`, workspaceID, generation).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// CacheSample summarizes a single registry response without recording its path.
type CacheSample struct {
	StartedAt, FinishedAt      time.Time
	CachedBytes, UpstreamBytes int64
	Status                     int
}

// RecordCache accumulates a bounded per-launch summary across parallel layer downloads.
func RecordCache(ctx context.Context, db writer, operationID string, sample CacheSample) error {
	cached, failed, rateLimited := 0, 0, 0
	if sample.CachedBytes > 0 {
		cached = 1
	}
	if sample.Status >= 400 || sample.Status == 0 {
		failed = 1
	}
	if sample.Status == 429 {
		rateLimited = 1
	}
	_, err := db.ExecContext(ctx, `INSERT INTO launch_cache_stats
		(operation_id,requests,cached_requests,failed_requests,rate_limited_requests,cached_bytes,upstream_bytes,first_request_at,last_response_at)
		SELECT o.id,1,$2,$3,$4,$5,$6,$7,$8 FROM operations o WHERE o.id=$1 AND `+startupOperation+`
		ON CONFLICT(operation_id) DO UPDATE SET requests=launch_cache_stats.requests+1,
		  cached_requests=launch_cache_stats.cached_requests+excluded.cached_requests,
		  failed_requests=launch_cache_stats.failed_requests+excluded.failed_requests,
		  rate_limited_requests=launch_cache_stats.rate_limited_requests+excluded.rate_limited_requests,
		  cached_bytes=launch_cache_stats.cached_bytes+excluded.cached_bytes,
		  upstream_bytes=launch_cache_stats.upstream_bytes+excluded.upstream_bytes,
		  first_request_at=CASE WHEN excluded.first_request_at<launch_cache_stats.first_request_at
		    THEN excluded.first_request_at ELSE launch_cache_stats.first_request_at END,
		  last_response_at=CASE WHEN excluded.last_response_at>launch_cache_stats.last_response_at
		    THEN excluded.last_response_at ELSE launch_cache_stats.last_response_at END
		WHERE launch_cache_stats.requests<4096`, operationID, cached, failed, rateLimited, sample.CachedBytes,
		sample.UpstreamBytes, stamp(sample.StartedAt), stamp(sample.FinishedAt))
	return err
}

// WorkspaceOperation identifies the latest admitted launch or start for the workspace.
func WorkspaceOperation(ctx context.Context, db reader, workspaceID string) (*string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT o.id FROM operations o WHERE o.workspace_id=$1
		AND `+startupOperation+` ORDER BY o.created_at DESC,o.id DESC LIMIT 1`, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
