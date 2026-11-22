package guest

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
)

const (
	firstTrialOperation = "99999999-0000-4000-8000-000000000001"
	nextTrialOperation  = "99999999-0000-4000-8000-000000000002"
)

// TestActivateStagedGuestUpdate follows the staged operation across a
// supervisor restart and acknowledges success only after trial confirmation.
func TestActivateStagedGuestUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operation.json")
	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("staged fixture"))
	dispatch := guestDispatch{ID: "99999999-0000-4000-8000-000000000001", Generation: 1, Version: 2,
		Command: stageGuestUpdateCommand, ProductRelease: "candidate"}
	if err := journal.stageUpdate(t.Context(), dispatch, func(context.Context, string) ([sha256.Size]byte, error) {
		return digest, nil
	}); err != nil {
		t.Fatal(err)
	}
	activate := dispatch
	activate.Command = "activate-guest-update"
	other := activate
	other.ProductRelease = "other"
	if err := journal.activateUpdate(other); !errors.Is(err, errGuestDispatchConflict) {
		t.Errorf("activation of a different release = %v, want conflict", err)
	}
	if err := journal.activateUpdate(activate); !errors.Is(err, errGuestUpdateRestart) {
		t.Fatalf("first activation = %v, want supervised restart", err)
	}
	trialPath := filepath.Join(dir, "update-trial.json")
	boot, err := nextGuestUpdateBinary(trialPath)
	if err != nil || boot.Digest != digest {
		t.Fatalf("trial boot = %+v (%v), want staged digest", boot, err)
	}
	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.activateUpdate(activate); err != nil {
		t.Fatal(err)
	}
	if report := journal.report(); report.Step != "update.stage" {
		t.Errorf("unconfirmed trial report = %+v, want staging receipt only", report)
	}
	if err := confirmGuestUpdateTrial(trialPath, boot); err != nil {
		t.Fatal(err)
	}
	if err := journal.activateUpdate(activate); err != nil {
		t.Fatal(err)
	}
	if report := journal.report(); report.Step != "update.activate" || report.Status != "succeeded" {
		t.Errorf("confirmed trial report = %+v, want successful activation", report)
	}
}

// TestActivateStagedGuestUpdateReportsRollback checks that redelivery records
// the failed trial and does not start another one.
func TestActivateStagedGuestUpdateReportsRollback(t *testing.T) {
	dir := t.TempDir()
	journal, err := loadLocalJournal(filepath.Join(dir, "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("failed candidate"))
	dispatch := guestDispatch{ID: "99999999-0000-4000-8000-000000000001", Generation: 1, Version: 2,
		Command: stageGuestUpdateCommand, ProductRelease: "candidate"}
	if err := journal.stageUpdate(t.Context(), dispatch, func(context.Context, string) ([sha256.Size]byte, error) {
		return digest, nil
	}); err != nil {
		t.Fatal(err)
	}
	dispatch.Command = activateGuestUpdateCommand
	if err := journal.activateUpdate(dispatch); !errors.Is(err, errGuestUpdateRestart) {
		t.Fatalf("first activation = %v, want supervised restart", err)
	}
	trial := filepath.Join(dir, "update-trial.json")
	for range 2 {
		if _, err := nextGuestUpdateBinary(trial); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := journal.activateUpdate(dispatch); err != nil {
			t.Fatal(err)
		}
		if report := journal.report(); report.Step != "update.activate" || report.Status != "failed" || report.Problem != problemGuestUpdateRollback {
			t.Errorf("rolled-back trial report = %+v, want rollback evidence", report)
		}
	}
	// An administrator's replacement campaign uses a fresh operation ID.
	// Staging the same signed binary must permit one new supervised trial.
	replacement := dispatch
	replacement.ID = "99999999-0000-4000-8000-000000000002"
	replacement.Command = stageGuestUpdateCommand
	if err := journal.stageUpdate(t.Context(), replacement, func(context.Context, string) ([sha256.Size]byte, error) {
		return digest, nil
	}); err != nil {
		t.Fatal(err)
	}
	journal, err = loadLocalJournal(filepath.Join(dir, "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	replacement.Command = activateGuestUpdateCommand
	if err := journal.activateUpdate(replacement); !errors.Is(err, errGuestUpdateRestart) {
		t.Fatalf("activation for replacement operation = %v, want one new trial", err)
	}
	boot, err := nextGuestUpdateBinary(trial)
	if err != nil || boot.Digest != digest {
		t.Fatalf("replacement trial = %+v (%v), want same verified digest", boot, err)
	}
	if _, err := nextGuestUpdateBinary(trial); err != nil {
		t.Fatal(err)
	}
	if err := journal.activateUpdate(replacement); err != nil {
		t.Fatalf("replacement replay = %v, want rollback recorded without a new trial", err)
	}
	if report := journal.report(); report.Status != "failed" || report.Problem != problemGuestUpdateRollback {
		t.Errorf("replacement rollback = %+v, want failed activation", report)
	}
}

// TestGuestUpdateTrialRollback verifies that an interrupted trial falls back
// on the next supervisor start and cannot be retried by repeating the request.
func TestGuestUpdateTrialRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-trial.json")
	digest := sha256.Sum256([]byte("unhealthy update"))
	if err := requestGuestUpdateTrial(path, digest, firstTrialOperation); err != nil {
		t.Fatal(err)
	}
	first, err := nextGuestUpdateBinary(path)
	if err != nil || first.Digest != digest || first.Attempt == "" {
		t.Fatalf("first supervisor start = %+v (%v), want candidate and attempt token", first, err)
	}
	fallback, err := nextGuestUpdateBinary(path)
	if err != nil || fallback != (guestUpdateBoot{}) {
		t.Fatalf("supervisor restart = %x (%v), want original agent", fallback, err)
	}
	if err := confirmGuestUpdateTrial(path, first); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("late trial confirmation = %v, want conflict", err)
	}
	if err := requestGuestUpdateTrial(path, digest, firstTrialOperation); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("automatic retry of failed candidate = %v, want conflict", err)
	}
	if err := requestGuestUpdateTrial(path, digest, nextTrialOperation); err != nil {
		t.Fatalf("explicit trial retry = %v, want success", err)
	}
	retry, err := nextGuestUpdateBinary(path)
	if err != nil || retry.Digest != digest || retry.Attempt == "" || retry.Attempt == first.Attempt {
		t.Fatalf("retried boot = %+v (%v), want same digest with a new attempt token", retry, err)
	}
	if err := confirmGuestUpdateTrial(path, first); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("stale confirmation during retry = %v, want conflict", err)
	}
}

// TestGuestUpdateTrialKeepsLastHealthyBinary proves that confirmation is tied
// to the attempted binary and a later failed trial preserves the healthy one.
func TestGuestUpdateTrialKeepsLastHealthyBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-trial.json")
	first := sha256.Sum256([]byte("healthy update"))
	second := sha256.Sum256([]byte("next update"))
	if err := requestGuestUpdateTrial(path, first, firstTrialOperation); err != nil {
		t.Fatal(err)
	}
	boot, err := nextGuestUpdateBinary(path)
	if err != nil || boot.Digest != first {
		t.Fatalf("first candidate = %x (%v), want %x", boot.Digest, err, first)
	}
	if err := confirmGuestUpdateTrial(path, guestUpdateBoot{Digest: second, Attempt: boot.Attempt}); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("confirm with another candidate digest = %v, want conflict", err)
	}
	if err := confirmGuestUpdateTrial(path, boot); err != nil {
		t.Fatal(err)
	}
	if err := confirmGuestUpdateTrial(path, boot); err != nil {
		t.Fatalf("replay successful confirmation = %v, want success", err)
	}
	if err := requestGuestUpdateTrial(path, second, nextTrialOperation); err != nil {
		t.Fatal(err)
	}
	if got, err := nextGuestUpdateBinary(path); err != nil || got.Digest != second {
		t.Fatalf("second candidate = %x (%v), want %x", got.Digest, err, second)
	}
	if got, err := nextGuestUpdateBinary(path); err != nil || got != (guestUpdateBoot{Digest: first}) {
		t.Fatalf("restart after failed candidate = %+v (%v), want last confirmed %x", got, err, first)
	}
}
