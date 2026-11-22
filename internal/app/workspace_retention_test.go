package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestWorkspaceRetentionSchedule holds deletion through startup and resets
// grace when the provider fails or the controller misses a cycle.
func TestWorkspaceRetentionSchedule(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	limit := int64(3600)
	calls := 0
	probes := 0
	var probeErr error
	state := &ControllerState{retentionMaximum: &limit, retentionGrace: 10 * time.Minute}
	scheduler := workspaceRetentionSchedule{
		maximum: &limit, grace: 10 * time.Minute, now: func() time.Time { return now },
		publish: func(snapshot retentionSnapshot) { state.retentionStatus.Store(&snapshot) },
		probe: func(context.Context) (provider.Capabilities, error) {
			probes++
			return provider.Capabilities{Stop: true, Delete: true}, probeErr
		},
	}
	scheduler.process = func(context.Context, time.Time, time.Time) error {
		calls++
		return nil
	}
	tick := func(elapsed time.Duration, want int) {
		t.Helper()
		now = now.Add(elapsed)
		if err := scheduler.tick(t.Context()); err != nil || calls != want {
			t.Fatalf("schedule tick at %v = %d processed (%v), want %d", now, calls, err, want)
		}
	}
	check := func(want string, until time.Time) {
		t.Helper()
		got := state.workspaceRetentionStatus(now)
		if got.State != want || (got.GraceUntil == nil && !until.IsZero()) ||
			(got.GraceUntil != nil && !got.GraceUntil.Equal(until)) {
			t.Fatalf("retention status = %+v, want %s through %v", got, want, until)
		}
	}
	check("paused", time.Time{})
	tick(0, 0)
	check("grace", now.Add(10*time.Minute))
	for i := 1; i <= 9; i++ {
		tick(time.Minute, 0)
	}
	tick(time.Minute, 1)
	check("ready", time.Time{})
	probeErr = errors.New("fixture provider outage")
	tick(time.Minute, 1)
	check("paused", time.Time{})
	probeErr = nil
	tick(time.Minute, 1)
	for i := 1; i <= 9; i++ {
		tick(time.Minute, 1)
	}
	tick(time.Minute, 2)
	check("ready", time.Time{})
	tick(5*time.Minute, 2)
	check("grace", now.Add(10*time.Minute))
	checked := probes
	scheduler.maximum = nil
	tick(time.Hour, 2)
	if probes != checked+1 {
		t.Fatal("disabled deletion must still probe the provider for inactivity shutdown")
	}
}

// TestInactivityScheduleWithoutDeletion keeps inactivity shutdown running when
// destructive retention is disabled and the provider supports only stop.
func TestInactivityScheduleWithoutDeletion(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	scheduler := workspaceRetentionSchedule{
		grace: time.Minute, now: func() time.Time { return now },
		probe:   func(context.Context) (provider.Capabilities, error) { return provider.Capabilities{Stop: true}, nil },
		process: func(context.Context, time.Time, time.Time) error { calls++; return nil },
	}
	if err := scheduler.tick(t.Context()); err != nil || calls != 0 {
		t.Fatalf("startup tick = %v, %d calls, want recovery grace", err, calls)
	}
	now = now.Add(time.Minute)
	if err := scheduler.tick(t.Context()); err != nil || calls != 1 {
		t.Errorf("inactivity tick without deletion = %v, %d calls, want one shutdown pass", err, calls)
	}
}
