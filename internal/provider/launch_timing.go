package provider

import (
	"context"
	"time"
)

type launchTimingKey struct{}

// WithLaunchTiming attaches an optional observer to an admitted launch's provider calls.
func WithLaunchTiming(ctx context.Context, observe func(phase string, started, finished time.Time, err error)) context.Context {
	return context.WithValue(ctx, launchTimingKey{}, observe)
}

// ReportLaunchTiming emits the timing of a provider subphase to the controller.
func ReportLaunchTiming(ctx context.Context, phase string, started time.Time, err error) {
	if observe, ok := ctx.Value(launchTimingKey{}).(func(string, time.Time, time.Time, error)); ok {
		observe(phase, started, time.Now(), err)
	}
}
