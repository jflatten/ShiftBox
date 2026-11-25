package launchlog

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Performance joins authoritative journal timestamps with supplemental diagnostics.
type Performance struct {
	TimeToIDEReadyMS          *int64  `json:"timeToIdeReadyMs,omitempty"`
	TimeToSetupCompleteMS     *int64  `json:"timeToSetupCompleteMs,omitempty"`
	VMRunningToGuestContactMS *int64  `json:"vmRunningToGuestContactMs,omitempty"`
	Steps                     []Step  `json:"steps"`
	Events                    []Event `json:"events"`
	Cache                     *Cache  `json:"cache,omitempty"`
}

// Step projects a durable controller workflow step.
type Step struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	DurationMS int64  `json:"durationMs"`
	StartedAt  string `json:"startedAt,omitempty"`
}

// Event is one supplemental controller, provider, or guest timing.
type Event struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Phase      string    `json:"phase"`
	Outcome    string    `json:"outcome"`
	OccurredAt time.Time `json:"occurredAt"`
	DurationMS int64     `json:"durationMs"`
	ReasonCode string    `json:"reasonCode,omitempty"`
}

// Cache summarizes registry traffic during the launch, including overlapping requests.
type Cache struct {
	Requests            int64 `json:"requests"`
	CachedRequests      int64 `json:"cachedRequests"`
	FailedRequests      int64 `json:"failedRequests"`
	RateLimitedRequests int64 `json:"rateLimitedRequests"`
	CachedBytes         int64 `json:"cachedBytes"`
	UpstreamBytes       int64 `json:"upstreamBytes"`
	WindowMS            int64 `json:"windowMs"`
}

// Read calculates launch milestones in controller time. Historical workflow
// timings remain available even for launches predating supplemental collection.
func Read(ctx context.Context, db reader, operationID string, now time.Time) (Performance, error) {
	info := Performance{Steps: []Step{}, Events: []Event{}}
	if err := readSteps(ctx, db, operationID, now, &info); err != nil {
		return Performance{}, err
	}
	if err := readEvents(ctx, db, operationID, &info); err != nil {
		return Performance{}, err
	}
	var cache Cache
	var first, last string
	err := db.QueryRowContext(ctx, `SELECT requests,cached_requests,failed_requests,rate_limited_requests,
		cached_bytes,upstream_bytes,first_request_at,last_response_at FROM launch_cache_stats WHERE operation_id=$1`, operationID).
		Scan(&cache.Requests, &cache.CachedRequests, &cache.FailedRequests, &cache.RateLimitedRequests,
			&cache.CachedBytes, &cache.UpstreamBytes, &first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return info, nil
	}
	if err != nil {
		return Performance{}, err
	}
	window, err := timeBetween(first, last)
	if err != nil {
		return Performance{}, err
	}
	cache.WindowMS = window
	info.Cache = &cache
	return info, nil
}

func readSteps(ctx context.Context, db reader, operationID string, now time.Time, info *Performance) error {
	rows, err := db.QueryContext(ctx, `SELECT s.step_key,s.label,s.status,s.started_at,s.finished_at,o.created_at
		FROM operation_steps s JOIN operations o ON o.id=s.operation_id
		WHERE s.operation_id=$1 AND `+startupOperation+` ORDER BY s.step_index`, operationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var step Step
		var started, finished sql.NullString
		var admitted string
		if err := rows.Scan(&step.Key, &step.Label, &step.Status, &started, &finished, &admitted); err != nil {
			return err
		}
		if started.Valid {
			step.StartedAt = started.String
			end := stamp(now)
			if finished.Valid {
				end = finished.String
			}
			step.DurationMS, err = timeBetween(started.String, end)
			if err != nil {
				return err
			}
		}
		info.Steps = append(info.Steps, step)
		if step.Status != "succeeded" || !finished.Valid || (step.Key != "verify_ide" && step.Key != "finish_setup") {
			continue
		}
		duration, err := timeBetween(admitted, finished.String)
		if err != nil {
			return err
		}
		if step.Key == "verify_ide" {
			info.TimeToIDEReadyMS = &duration
		} else {
			info.TimeToSetupCompleteMS = &duration
		}
	}
	return rows.Err()
}

func readEvents(ctx context.Context, db reader, operationID string, info *Performance) error {
	rows, err := db.QueryContext(ctx, `SELECT id,source,phase,outcome,occurred_at,duration_ms,reason_code
		FROM launch_timing_events WHERE operation_id=$1 ORDER BY occurred_at,source,id`, operationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var running, authenticated time.Time
	for rows.Next() {
		var event Event
		var occurred string
		if err := rows.Scan(&event.ID, &event.Source, &event.Phase, &event.Outcome, &occurred, &event.DurationMS, &event.ReasonCode); err != nil {
			return err
		}
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			return err
		}
		switch event.Phase {
		case "vm_running":
			running = event.OccurredAt
		case "guest_authenticated":
			authenticated = event.OccurredAt
		}
		info.Events = append(info.Events, event)
	}
	if !running.IsZero() && !authenticated.Before(running) {
		duration := authenticated.Sub(running).Milliseconds()
		info.VMRunningToGuestContactMS = &duration
	}
	return rows.Err()
}

func timeBetween(first, last string) (int64, error) {
	start, err := time.Parse(time.RFC3339Nano, first)
	if err != nil {
		return 0, err
	}
	end, err := time.Parse(time.RFC3339Nano, last)
	if err != nil {
		return 0, err
	}
	return max(0, end.Sub(start).Milliseconds()), nil
}
