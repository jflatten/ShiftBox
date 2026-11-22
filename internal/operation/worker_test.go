package operation_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestWorkerSchedulesOnlyOwnedActions checks that a controller worker resumes
// its own work without taking leases on setup or future operation types.
func TestWorkerSchedulesOnlyOwnedActions(t *testing.T) {
	journal, _ := journalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var setupID string
	for _, action := range []string{"setup.activate", "workspace.launch"} {
		input := provisionRequest()
		input.Key += action
		input.OriginatingAction = action
		input.Steps = input.Steps[:1]
		if action == "setup.activate" {
			input.Type = "setup"
		}
		op, err := journal.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if input.Type == "setup" {
			setupID = op.ID
		}
	}
	effect := &actionEffect{seen: make(chan operation.Operation, 2)}
	worker := operation.Worker{Journal: journal, Name: "controller", LeaseDuration: time.Minute, Executor: effect,
		Actions: []string{"workspace.launch"}}
	if err := worker.RunOnce(ctx, setupID); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("RunOnce for setup = %v, want conflict", err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := worker.Run(ctx, time.Millisecond); !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run = %v, want cancellation", err)
		}
	})
	defer func() { cancel(); wg.Wait() }()
	select {
	case op := <-effect.seen:
		if op.OriginatingAction != "workspace.launch" {
			t.Fatalf("first operation = %s, want workspace.launch", op.OriginatingAction)
		}
	case <-ctx.Done():
		t.Fatal("workspace.launch did not run")
	}
	setup, err := journal.Get(ctx, setupID)
	if err != nil || setup.Status != "pending" || setup.Version != 1 {
		t.Fatalf("setup after workspace worker ran = %s version %d, %v, want the original pending intent", setup.Status, setup.Version, err)
	}
}

type actionEffect struct{ seen chan operation.Operation }

func (e *actionEffect) Observe(_ context.Context, op operation.Operation, _ operation.Step) (string, bool, error) {
	e.seen <- op
	return "verified", true, nil
}

func (e *actionEffect) Apply(context.Context, operation.Operation, operation.Step) (string, error) {
	return "", errors.New("unexpected apply")
}
