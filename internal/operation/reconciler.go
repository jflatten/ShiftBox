package operation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// Executor reconciles one approved effect. Observe must inspect authoritative
// facts; Apply must honor cancellation and never infer success from a timeout.
type Executor interface {
	Observe(context.Context, Operation, Step) (evidence string, completed bool, err error)
	Apply(context.Context, Operation, Step) (evidence string, err error)
}

// StepFailure is a terminal effect outcome with a public, bounded description.
type StepFailure struct {
	Detail string
}

func (f *StepFailure) Error() string { return f.Detail }

// StepWaiting carries a public explanation for an effect that will be retried.
type StepWaiting struct {
	Detail string
	Cause  error
}

func (w *StepWaiting) Error() string { return w.Detail }

// Unwrap preserves the underlying cause for the controller.
func (w *StepWaiting) Unwrap() error { return w.Cause }

// Worker processes one operation step at a time with durable intent and fencing.
type Worker struct {
	Journal       *Journal
	Name          string
	LeaseDuration time.Duration
	Executor      Executor
	// Actions optionally limits the worker to the admission actions it owns.
	Actions []string
}

// RunOnce always observes before applying, including after a lost response.
func (w *Worker) RunOnce(ctx context.Context, id string) error {
	claim, err := w.Journal.claim(ctx, id, w.Name, w.LeaseDuration, w.Actions)
	if err != nil {
		return err
	}
	op, err := w.Journal.Get(ctx, id)
	if err != nil {
		return err
	}
	index := -1
	for i, step := range op.Steps {
		if step.Status == "pending" || step.Status == "active" {
			index = i
			break
		}
	}
	if index < 0 {
		return ErrConflict
	}
	if err := w.Journal.StartStep(ctx, claim, index); err != nil {
		return err
	}
	op, err = w.Journal.Get(ctx, id)
	if err != nil {
		return err
	}
	// The effect deadline ends before its lease. An expired result is rejected
	// even if an external implementation fails to honor cancellation.
	effectCtx, cancel := context.WithTimeout(ctx, w.LeaseDuration*2/3)
	defer cancel()
	evidence, completed, err := w.Executor.Observe(effectCtx, op, op.Steps[index])
	if err == nil && !completed {
		if err = w.Journal.Renew(ctx, claim, w.LeaseDuration); err == nil {
			evidence, err = w.Executor.Apply(effectCtx, op, op.Steps[index])
		}
	}
	if err != nil {
		if failure, ok := errors.AsType[*StepFailure](err); ok {
			return w.Journal.failStep(ctx, claim, index, failure.Detail)
		}
		detail := "Waiting for a verified observation before retrying."
		if waiting, ok := errors.AsType[*StepWaiting](err); ok && waiting.Detail != "" && len(waiting.Detail) <= 4096 {
			detail = waiting.Detail
		}
		waitErr := w.Journal.wait(ctx, claim, detail)
		return errors.Join(err, waitErr)
	}
	if err := w.Journal.CompleteStep(ctx, claim, index, evidence); err != nil {
		return err
	}
	if index+1 < len(op.Steps) {
		return w.Journal.wait(ctx, claim, "")
	}
	return nil
}

func (j *Journal) failStep(ctx context.Context, claim Claim, index int, detail string) error {
	return database.Transaction(ctx, j.db, func(tx *sql.Tx) error {
		return j.FailStepInTx(ctx, tx, claim, index, detail)
	})
}

// FailStepInTx records a terminal failure with related domain state.
func (j *Journal) FailStepInTx(ctx context.Context, tx *sql.Tx, claim Claim, index int, detail string) error {
	if detail == "" || len(detail) > 4096 {
		return errors.New("failure requires bounded non-secret evidence")
	}
	return j.changeInTx(ctx, tx, claim, detail, func(op *Operation) error {
		if index < 0 || index >= len(op.Steps) || op.Steps[index].Status != "active" {
			return ErrConflict
		}
		op.Steps[index].Status = "failed"
		op.Steps[index].Detail = detail
		op.Status = "failed"
		op.leaseOwner = ""
		op.leaseExpiresAt = time.Time{}
		now := j.now().UTC()
		op.FinishedAt = &now
		return nil
	})
}

func (j *Journal) wait(ctx context.Context, claim Claim, detail string) error {
	return j.change(ctx, claim, "", func(op *Operation) error {
		op.Status = "waiting"
		op.leaseOwner = ""
		op.leaseExpiresAt = time.Time{}
		for i := range op.Steps {
			if op.Steps[i].Status == "active" {
				op.Steps[i].Detail = detail
			}
		}
		return nil
	})
}
