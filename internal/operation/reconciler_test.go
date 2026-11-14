package operation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
)

type lostResponseEffect struct {
	journal *operation.Journal
	effects int
}

func (f *lostResponseEffect) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	return "provider confirms instance", f.effects > 0, nil
}

func (f *lostResponseEffect) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if step.Status != "active" {
		return "", errors.New("executor received stale pending step")
	}
	// A separate read would block if the worker still held its intent transaction.
	current, err := f.journal.Get(ctx, op.ID)
	if err != nil {
		return "", err
	}
	if current.Steps[step.Index].Status != "active" {
		return "", errors.New("external effect preceded durable active step")
	}
	f.effects++
	return "", errors.New("provider response lost after creating instance")
}

// TestReconcileLostResponse observes provider facts before retrying a committed effect.
func TestReconcileLostResponse(t *testing.T) {
	j, _ := journalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := provisionRequest()
	request.Steps = request.Steps[:1]
	op, err := j.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	effect := &lostResponseEffect{journal: j}
	worker := operation.Worker{Journal: j, Name: "controller", LeaseDuration: time.Minute, Executor: effect}
	if err := worker.RunOnce(ctx, op.ID); err == nil {
		t.Fatal("RunOnce(lost response) succeeded, want uncertainty")
	}
	current, err := j.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "waiting" {
		t.Errorf("uncertain operation status = %s, want waiting", current.Status)
	}
	// A replacement worker must reconcile persisted intent instead of repeating Create.
	restarted := operation.Worker{Journal: j, Name: "controller-after-restart", LeaseDuration: time.Minute, Executor: effect}
	if err := restarted.RunOnce(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if effect.effects != 1 {
		t.Errorf("provider effects = %d, want exactly 1", effect.effects)
	}
	current, err = j.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "succeeded" {
		t.Errorf("reconciled status = %s, want succeeded", current.Status)
	}
}

// TestWorkerCancellation stops scheduling before it can acquire work after shutdown.
func TestWorkerCancellation(t *testing.T) {
	j, _ := journalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := operation.Worker{Journal: j, Name: "controller", LeaseDuration: time.Minute, Executor: &lostResponseEffect{journal: j}}
	if err := worker.Run(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Errorf("Run(cancelled) = %v, want context cancellation", err)
	}
}
