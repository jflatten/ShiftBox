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
	scheduler := workspaceRetentionSchedule{
		maximum: &limit, grace: 10 * time.Minute, now: func() time.Time { return now },
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
	tick(0, 0)
	for i := 1; i <= 9; i++ {
		tick(time.Minute, 0)
	}
	tick(time.Minute, 1)
	probeErr = errors.New("fixture provider outage")
	tick(time.Minute, 1)
	probeErr = nil
	tick(time.Minute, 1)
	for i := 1; i <= 9; i++ {
		tick(time.Minute, 1)
	}
	tick(time.Minute, 2)
	tick(5*time.Minute, 2)
	checked := probes
	scheduler.maximum = nil
	tick(time.Hour, 2)
	if probes != checked {
		t.Fatal("disabled retention still inspected the provider")
	}
}
