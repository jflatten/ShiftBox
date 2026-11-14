package guest

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
)

// TestGuestUpdateTrialRollback verifies that an interrupted trial falls back
// on the next supervisor start and cannot be retried by repeating the request.
func TestGuestUpdateTrialRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-trial.json")
	digest := sha256.Sum256([]byte("unhealthy update"))
	if err := requestGuestUpdateTrial(path, digest, false); err != nil {
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
	if err := requestGuestUpdateTrial(path, digest, false); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("automatic retry of failed candidate = %v, want conflict", err)
	}
	if err := requestGuestUpdateTrial(path, digest, true); err != nil {
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
	if err := requestGuestUpdateTrial(path, first, false); err != nil {
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
	if err := requestGuestUpdateTrial(path, second, false); err != nil {
		t.Fatal(err)
	}
	if got, err := nextGuestUpdateBinary(path); err != nil || got.Digest != second {
		t.Fatalf("second candidate = %x (%v), want %x", got.Digest, err, second)
	}
	if got, err := nextGuestUpdateBinary(path); err != nil || got != (guestUpdateBoot{Digest: first}) {
		t.Fatalf("restart after failed candidate = %+v (%v), want last confirmed %x", got, err, first)
	}
}
