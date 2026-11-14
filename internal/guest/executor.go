package guest

import "context"

type localEvidence struct {
	ContainerID    string `json:"containerId,omitempty"`
	WaitForReached bool   `json:"waitForReached"`
}

type localReport struct {
	ID         string        `json:"operationId"`
	Generation int64         `json:"workspaceGeneration"`
	Status     string        `json:"status"`
	Step       string        `json:"step"`
	Attempt    int           `json:"attempt"`
	Evidence   localEvidence `json:"evidence"`
	Problem    string        `json:"problem,omitempty"`
}

func (j *localJournal) report() localReport {
	op := j.state.Operation
	if op == nil {
		return localReport{}
	}
	report := localReport{
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
	return report
}

// ensureDevelopment advances one lifecycle stage per dispatch. This lets the
// next poll carry the waitFor milestone even if later hooks are slow or fail.
func (j *localJournal) ensureDevelopment(ctx context.Context, dispatch guestDispatch, runtime devContainer) (localReport, error) {
	if err := ctx.Err(); err != nil {
		return localReport{}, err
	}
	if err := j.accept(dispatch); err != nil {
		return localReport{}, err
	}
	next := *j.state.Operation
	var effectErr error
	switch next.Phase {
	case localPending:
		next.Phase = localRequired
		if err := j.saveOperation(next); err != nil {
			return localReport{}, err
		}
		next.Connection, effectErr = runtime.ensure(ctx)
		next.Phase = localWaitReached
		if effectErr != nil {
			next.Phase, next.Problem = localFailed, problemRequired
		}
	case localWaitReached:
		next.Phase = localRemaining
		if err := j.saveOperation(next); err != nil {
			return localReport{}, err
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
		return localReport{}, ctx.Err()
	}
	if err := j.saveOperation(next); err != nil {
		return localReport{}, err
	}
	return j.report(), nil
}
