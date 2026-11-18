package operation_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"

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

type incompleteTransition struct {
	verify       bool
	observations int
	applies      int
}

func (e *incompleteTransition) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	e.observations++
	return "Provider verified the stop.", e.verify, nil
}

func (e *incompleteTransition) Apply(context.Context, operation.Operation, operation.Step) (string, error) {
	e.applies++
	return "", &operation.StepWaiting{Detail: "Waiting for provider confirmation."}
}

// TestTransitionDeadlineSurvivesRestart verifies that changing the worker's
// configured duration cannot extend entered work, and that late evidence
// completes the workflow while preserving the same timeout in journal reads.
func TestTransitionDeadlineSurvivesRestart(t *testing.T) {
	j, now, db, path := journalDatabase(t)
	request := provisionRequest()
	request.Type = "stop"
	request.Steps = []operation.Step{{Key: "shutdown", Label: "Stop workloads"}, {Key: "stop", Label: "Verify provider stop"}}
	op, err := j.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	effect := &incompleteTransition{verify: true}
	worker := operation.Worker{Journal: j, Name: "worker", LeaseDuration: time.Minute, Executor: effect,
		TransitionLimit: func(operation.Operation, int) operation.TransitionLimit {
			return operation.TransitionLimit{FirstStep: 0, Duration: time.Minute}
		},
	}
	check := func() operation.Operation {
		t.Helper()
		got, err := worker.Journal.Get(t.Context(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	deadline := now.Add(time.Minute)
	if err := worker.RunOnce(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	if got := check().Steps[0]; got.Status != "succeeded" || got.Timeout != nil {
		t.Fatalf("first step = %+v, want succeeded before deadline", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	*now = deadline
	worker.Journal = operation.NewJournal(reopened, func() time.Time { return *now }, uuid.NewString)
	worker.TransitionLimit = func(operation.Operation, int) operation.TransitionLimit {
		return operation.TransitionLimit{FirstStep: 0, Duration: time.Hour}
	}
	effect.verify = false
	var waiting *operation.StepWaiting
	if err := worker.RunOnce(t.Context(), op.ID); !errors.As(err, &waiting) {
		t.Fatalf("RunOnce at expiry = %v, want existing retry behavior", err)
	}
	expired := check()
	timeout := expired.Steps[1].Timeout
	if expired.Status != "waiting" || timeout == nil || !timeout.OccurredAt.Equal(deadline) || timeout.Reason == "" {
		t.Fatalf("operation after expiry = %s, timeout %+v, want waiting with original deadline %s", expired.Status, timeout, deadline)
	}
	if effect.observations != 2 || effect.applies != 1 {
		t.Fatalf("observations/actions = %d/%d, want final observation followed by ordinary retry", effect.observations, effect.applies)
	}
	*now = now.Add(time.Minute)
	if err := worker.RunOnce(t.Context(), op.ID); !errors.As(err, &waiting) {
		t.Fatalf("RunOnce after timeout = %v, want waiting", err)
	}
	effect.verify = true
	if err := worker.RunOnce(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	finished := check()
	if got := finished.Steps[1].Timeout; finished.Status != "succeeded" || got == nil || *got != *timeout {
		t.Fatalf("late completion = %s, timeout %+v, want succeeded with retained %+v", finished.Status, got, timeout)
	}
	events, err := worker.Journal.Events(t.Context(), op.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	latest := events[len(events)-1].Operation
	if latest.Steps[1].Timeout == nil || !latest.Steps[1].Timeout.OccurredAt.Equal(deadline) {
		t.Fatalf("latest event lost the original timeout: %+v", latest.Steps[1])
	}
}

// TestTransitionDeadlineUsesExistingActivation verifies an in-flight step from
// an older build keeps its original entry, and final verification at expiry
// takes precedence over recording a timeout.
func TestTransitionDeadlineUsesExistingActivation(t *testing.T) {
	j, now, db, _ := journalDatabase(t)
	request := provisionRequest()
	request.Steps = request.Steps[:1]
	op, err := j.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(t.Context(), op.ID, "old-controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(t.Context(), claim, 0); err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Minute)
	*now = deadline
	effect := &incompleteTransition{verify: true}
	worker := operation.Worker{Journal: j, Name: "new-controller", LeaseDuration: time.Minute, Executor: effect,
		TransitionLimit: func(operation.Operation, int) operation.TransitionLimit {
			return operation.TransitionLimit{FirstStep: 0, Duration: time.Minute}
		},
	}
	if err := worker.RunOnce(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	var persisted sql.NullString
	if err := db.QueryRow("SELECT transition_deadline FROM operation_steps WHERE operation_id=$1", op.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339Nano, persisted.String)
	if err != nil || !got.Equal(deadline) {
		t.Fatalf("legacy activation deadline = %s (%v), want %s", persisted.String, err, deadline)
	}
	verified, err := j.Get(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != "succeeded" || verified.Steps[0].Timeout != nil || effect.observations != 1 || effect.applies != 0 {
		t.Fatalf("verified at expiry = %s, step %+v, observations/actions %d/%d, want success after observation only", verified.Status, verified.Steps[0], effect.observations, effect.applies)
	}
}
