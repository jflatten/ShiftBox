package app

import (
	"context"
	"errors"
)

// ErrControllerIdentityStopped indicates that the managed Dex process exited.
var ErrControllerIdentityStopped = errors.New("controller identity service stopped")

// ServeController runs browser HTTPS, workspaces and history cleanup under
// the same lifetime as Dex. It joins the services before returning.
func ServeController(ctx context.Context, dexDone <-chan struct{}, browser, workspaces, history func(context.Context) error, additional ...func(context.Context) error) error {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	services := append([]func(context.Context) error{browser, workspaces, history}, additional...)
	results := make(chan error, len(services))
	for _, serve := range services {
		go func() { results <- serve(runCtx) }()
	}

	remaining := len(services)
	var cause error
	select {
	case <-ctx.Done():
		cause = ctx.Err()
	case <-dexDone:
		cause = ErrControllerIdentityStopped
	case cause = <-results:
		remaining--
		if cause == nil {
			cause = errors.New("controller service stopped unexpectedly")
		}
	}
	stop()
	for range remaining {
		<-results
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return cause
}
