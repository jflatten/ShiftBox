package app

import (
	"context"
	"errors"
)

// ErrControllerIdentityStopped indicates that the managed Dex process exited.
var ErrControllerIdentityStopped = errors.New("controller identity service stopped")

// ServeController runs browser HTTPS and the workspace runtime under the same
// lifetime as Dex. It joins the services before returning to the caller.
func ServeController(ctx context.Context, dexDone <-chan struct{}, browser, workspaces func(context.Context) error) error {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	results := make(chan error, 2)
	go func() { results <- browser(runCtx) }()
	go func() { results <- workspaces(runCtx) }()

	remaining := 2
	var cause error
	select {
	case <-ctx.Done():
		cause = ctx.Err()
	case <-dexDone:
		cause = ErrControllerIdentityStopped
	case cause = <-results:
		remaining--
		if cause == nil {
			cause = errors.New("controller listener stopped unexpectedly")
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
