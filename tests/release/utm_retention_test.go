//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestUTMRetentionDeletion verifies the shutdown grace and disk accounting
// when a native guest disconnects and the provider's delete reply is lost.
func TestUTMRetentionDeletion(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_RETENTION") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_RETENTION=1 for native retention deletion")
	}
	runUTMGuestFixture(t, nil, nativeRetention)
}

func verifyNativeRetention(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider, vm provider.Inventory, seconds int64) {
	t.Helper()
	id := vm.Ref.WorkspaceID
	// Keep the operation start behind real UTM observation timestamps. Only
	// the policy clock advances through the full graceful shutdown window.
	now := time.Now().UTC().Add(-10 * time.Second)
	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	store := workspace.NewStore(db)
	policy := workspace.RetentionEligibility{MaximumSeconds: &seconds, ReconciledAt: now, OutageGrace: time.Second}
	candidates := func() []workspace.RetentionCandidate {
		t.Helper()
		got, err := store.RetentionCandidates(ctx, vm.Ref.InstallationID, now, policy, machines.Inspect)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := candidates(); len(got) != 0 {
		t.Fatalf("retention discovered %d candidates inside outage grace", len(got))
	}
	now = now.Add(policy.OutageGrace)
	due := candidates()
	if len(due) != 1 || due[0].Ref != vm.Ref {
		t.Fatalf("overdue candidates = %+v, want the original VM", due)
	}
	// Feed a fresh observation through the production activity evaluator.
	sample := &workspace.CPUActivity{SampledAt: now, Percent: 25}
	if err := workspace.RecordActivity(ctx, db, id, vm.Ref.Generation, now,
		workspace.ActivityPolicy{CPU: true, CPUThreshold: 10, Freshness: time.Minute}, sample, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitRetentionDeletion(ctx, journal, due[0], now, policy); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("delete after fresh CPU activity = %v, want stale-candidate refusal", err)
	}
	if got := candidates(); len(got) != 0 {
		t.Fatalf("fresh CPU activity left %d due candidates", len(got))
	}
	now = now.Add(time.Duration(seconds) * time.Second)
	due = candidates()
	if len(due) != 1 || due[0].Ref != vm.Ref {
		t.Fatalf("candidates at the renewed deadline = %+v, want the original VM", due)
	}
	op, err := store.CommitRetentionDeletion(ctx, journal, due[0], now, policy)
	if err != nil {
		t.Fatal(err)
	}
	if op.OriginatingAction != "workspace.retention_delete" {
		t.Fatalf("deletion authority = %s, want retention", op.OriginatingAction)
	}
	t.Log("outage grace withheld deletion and a fresh activity sample invalidated the first candidate")

	adapter := &nativeRetentionAdapter{Provider: machines}
	worker := workspace.NewWorker(journal, nil, workspace.NewPowerExecutor(store, adapter, clock, nil))
	run := func(want error) {
		t.Helper()
		got := worker.RunOnce(ctx, op.ID)
		if !errors.Is(got, want) {
			t.Fatalf("deletion step = %v, want %v", got, want)
		}
	}
	for _, offset := range []time.Duration{0, 59 * time.Second} {
		now = now.Add(offset)
		err := worker.RunOnce(ctx, op.ID)
		if _, pending := errors.AsType[*operation.StepWaiting](err); !pending || adapter.forced != 0 {
			t.Fatalf("graceful shutdown at %v: %v and %d forced stops, want waiting with no force", offset, err, adapter.forced)
		}
	}
	now = now.Add(time.Second)
	run(nil)
	if err := waitNativeRetentionStop(ctx, worker, op.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := machines.Inspect(ctx, vm.Ref)
	if err != nil || stopped.Power != provider.Stopped || adapter.forced != 1 {
		t.Fatalf("retention stop = %s and %d forced stops (%v), want stopped after one authorized force", stopped.Power, adapter.forced, err)
	}
	verifyOwnedDisk(t, vm, stopped)
	checkNativeRetentionReservation(t, ctx, db, id, "reserved")
	t.Log("the authorized provider stop ran after the complete graceful shutdown window")

	run(provider.ErrPartial)
	remaining, err := machines.Inspect(ctx, vm.Ref)
	if err != nil || remaining.Power != provider.Stopped {
		t.Fatalf("inventory after partial cleanup = %s (%v), want stopped with its disk", remaining.Power, err)
	}
	verifyOwnedDisk(t, vm, remaining)
	checkNativeRetentionReservation(t, ctx, db, id, "reserved")

	run(provider.ErrOutcomeUnknown)
	if _, err := machines.Inspect(ctx, vm.Ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("inspection after losing the native deletion reply = %v, want confirmed provider absence", err)
	}
	checkNativeRetentionReservation(t, ctx, db, id, "reserved")
	run(nil)
	checkNativeRetentionReservation(t, ctx, db, id, "released")
	finished, err := journal.Get(ctx, op.ID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("retention operation = %s (%v), want successful verified cleanup", finished.Status, err)
	}
	t.Log("partial cleanup and a lost reply retained the disk reservation until the native adapter verified removal")
}

// waitNativeRetentionStop lets the real provider finish a transitional stop
// before exercising the deletion reply failures.
func waitNativeRetentionStop(ctx context.Context, worker *operation.Worker, id string) error {
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		err := worker.RunOnce(wait, id)
		if err == nil {
			return nil
		}
		pending, ok := errors.AsType[*operation.StepWaiting](err)
		if !ok {
			return err
		}
		select {
		case <-wait.Done():
			return errors.Join(wait.Err(), pending.Cause)
		case <-ticker.C:
		}
	}
}

func checkNativeRetentionReservation(t *testing.T, ctx context.Context, db *sql.DB, id, wantDisk string) {
	t.Helper()
	var compute, disk string
	err := db.QueryRowContext(ctx, "SELECT cpu_memory_state,disk_state FROM reservations WHERE workspace_id=$1", id).Scan(&compute, &disk)
	if err != nil || compute != "released" || disk != wantDisk {
		t.Fatalf("reservations = %s/%s (%v), want released/%s", compute, disk, err, wantDisk)
	}
}

type nativeRetentionAdapter struct {
	provider.Provider
	forced, deletes int
}

func (a *nativeRetentionAdapter) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	if req.ForceRetentionDelete {
		a.forced++
	}
	return a.Provider.Stop(ctx, req)
}

func (a *nativeRetentionAdapter) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	a.deletes++
	if a.deletes == 1 {
		return provider.Inventory{}, provider.ErrPartial
	}
	result, err := a.Provider.Delete(ctx, req)
	if err == nil && a.deletes == 2 {
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return result, err
}
