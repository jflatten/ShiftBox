package guest

import (
	"context"
	"errors"
)

type localEvidence struct {
	ContainerID    string `json:"containerId,omitempty"`
	WaitForReached bool   `json:"waitForReached"`
}

// OperationReport contains bounded evidence for an approved guest command.
type OperationReport struct {
	ID         string        `json:"operationId"`
	Generation int64         `json:"workspaceGeneration"`
	Status     string        `json:"status"`
	Step       string        `json:"step"`
	Attempt    int           `json:"attempt"`
	Evidence   localEvidence `json:"evidence"`
	Problem    string        `json:"problem,omitempty"`
}

func (j *localJournal) report() OperationReport {
	op := j.state.Operation
	if op == nil {
		return OperationReport{}
	}
	report := OperationReport{
		ID: op.ID, Generation: op.Generation, Status: "running", Attempt: op.Attempt, Problem: op.Problem,
		Step: "devcontainer.wait_for",
		Evidence: localEvidence{
			ContainerID: op.Connection.ID, WaitForReached: op.Connection.ID != "",
		},
	}
	if op.Phase == localRemaining || op.Phase == localSucceeded || op.Phase == localFailed && report.Evidence.WaitForReached {
		report.Step = "devcontainer.lifecycle"
	}
	if op.Phase == localSucceeded || op.Phase == localFailed {
		report.Status = string(op.Phase)
	}
	if op.Command == "shutdown-for-stop" {
		report.Step = "workload.shutdown"
	}
	return report
}

// ensureDevelopment advances one lifecycle stage per dispatch. This lets the
// next poll carry the waitFor milestone even if later hooks are slow or fail.
func (j *localJournal) ensureDevelopment(ctx context.Context, dispatch guestDispatch, runtime devContainer) (OperationReport, error) {
	if err := ctx.Err(); err != nil {
		return OperationReport{}, err
	}
	if err := j.accept(dispatch); err != nil {
		return OperationReport{}, err
	}
	next := *j.state.Operation
	var effectErr error
	switch next.Phase {
	case localPending:
		rebuild := dispatch.Command == rebuildDevelopmentCommand
		if runtime.repository != nil {
			source := repositorySource{URL: dispatch.Repository, Ref: dispatch.Ref, Config: dispatch.Config}
			var err error
			if rebuild {
				_, err = runtime.repository.prepared(source)
			} else {
				_, err = runtime.repository.prepare(ctx, source)
			}
			if err != nil {
				if ctx.Err() != nil {
					return OperationReport{}, ctx.Err()
				}
				next.Phase, next.Problem = localFailed, problemRepository
				break
			}
		}
		next.Phase = localRequired
		if err := j.saveOperation(next); err != nil {
			return OperationReport{}, err
		}
		if rebuild {
			next.Connection, effectErr = runtime.rebuild(ctx)
		} else {
			next.Connection, effectErr = runtime.ensure(ctx)
		}
		next.Phase = localWaitReached
		if effectErr != nil {
			next.Phase, next.Problem = localFailed, problemRequired
			if errors.Is(effectErr, errBrowserIDE) {
				next.Problem = problemIDE
			}
		}
	case localWaitReached:
		next.Phase = localRemaining
		if err := j.saveOperation(next); err != nil {
			return OperationReport{}, err
		}
		effectErr = runtime.finishSetup(ctx, next.Connection)
		next.Phase = localSucceeded
		if effectErr != nil {
			next.Phase, next.Problem = localFailed, problemRemaining
		}
	case localRequired, localRemaining:
		// The CLI records hook markers before executing hooks. Replaying after
		// interruption could skip a partial hook and certify incorrect readiness.
		next.Phase, next.Problem = localFailed, problemInterrupted
	case localSucceeded, localFailed:
		return j.report(), nil
	}
	if effectErr != nil && ctx.Err() != nil {
		return OperationReport{}, ctx.Err()
	}
	if err := j.saveOperation(next); err != nil {
		return OperationReport{}, err
	}
	return j.report(), nil
}
