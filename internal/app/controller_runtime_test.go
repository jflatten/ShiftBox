package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestControllerStopsOnIdentityExit proves an unexpected Dex exit stops both
// listeners and reconciliation before the controller releases shared state.
func TestControllerStopsOnIdentityExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	serve := func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	}
	dexDone := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- ServeController(ctx, dexDone, serve, serve) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("controller services did not start")
		}
	}
	close(dexDone)
	select {
	case err := <-result:
		if !errors.Is(err, ErrControllerIdentityStopped) || len(stopped) != 2 {
			t.Errorf("ServeController(Dex exit) = %v, stopped %d services, want identity failure and both services joined", err, len(stopped))
		}
	case <-ctx.Done():
		t.Fatal("controller did not stop after identity exit")
	}
}

// TestControllerPrimaryFailureKeepsCause ensures a listener error cancels
// its peer while preserving the original cause for the operator.
func TestControllerPrimaryFailureKeepsCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cause := errors.New("fixture listener stopped")
	stopped := make(chan struct{})
	err := ServeController(ctx, make(chan struct{}),
		func(context.Context) error { return cause },
		func(ctx context.Context) error {
			defer close(stopped)
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if !errors.Is(err, cause) {
		t.Errorf("ServeController(listener error) = %v, want original failure", err)
	}
	select {
	case <-stopped:
	default:
		t.Error("ServeController returned before the workspace service stopped")
	}
}
