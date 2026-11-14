package guest

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	shutdownTimeout        = 30 * time.Second
	problemShutdownTimeout = "guest.shutdown_timed_out"
	problemShutdownFailed  = "guest.shutdown_failed"
)

func (j *localJournal) shutdown(ctx context.Context, dispatch guestDispatch, runtime devContainer) (OperationReport, error) {
	if err := ctx.Err(); err != nil {
		return OperationReport{}, err
	}
	if dispatch.Command != "shutdown-for-stop" {
		return OperationReport{}, errGuestDispatchConflict
	}
	if err := j.accept(dispatch); err != nil {
		return OperationReport{}, err
	}
	next := *j.state.Operation
	if next.Phase == localSucceeded || next.Phase == localFailed {
		return j.report(), nil
	}
	next.Phase = localRequired
	if err := j.saveOperation(next); err != nil {
		return OperationReport{}, err
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	err := runtime.stopWorkload(shutdownCtx)
	if ctx.Err() != nil {
		return OperationReport{}, ctx.Err()
	}
	switch {
	case err == nil:
		next.Phase = localSucceeded
	case errors.Is(err, context.DeadlineExceeded):
		next.Phase, next.Problem = localFailed, problemShutdownTimeout
	default:
		next.Phase, next.Problem = localFailed, problemShutdownFailed
	}
	if err := j.saveOperation(next); err != nil {
		return OperationReport{}, err
	}
	return j.report(), nil
}

func (d devContainer) stopWorkload(ctx context.Context) error {
	run := d.run
	if run == nil {
		run = runDevContainerCLI
	}
	const docker = "/usr/bin/docker"
	out, err := run(ctx, docker, "container", "ls", "--quiet", "--no-trunc", "--filter", "label="+developmentLabel)
	if err != nil {
		return shutdownError(ctx)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return nil
	}
	if !dockerContainerID.MatchString(id) {
		return errors.New("unexpected development container inventory")
	}
	// Let the guest deadline bound the wait without Docker escalating to SIGKILL.
	if _, err := run(ctx, docker, "container", "stop", "--signal", "SIGTERM", "--timeout", "-1", "--", id); err != nil {
		return shutdownError(ctx)
	}
	return nil
}

func shutdownError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("workload shutdown could not be confirmed")
}
