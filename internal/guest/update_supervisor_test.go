package guest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestConfirmRunningGuestTrial requires the selected candidate to complete an
// authenticated poll before it can replace the last confirmed agent.
func TestConfirmRunningGuestTrial(t *testing.T) {
	dir := t.TempDir()
	digest := sha256.Sum256([]byte("fixture agent"))
	path := filepath.Join(dir, "update-trial.json")
	if err := requestGuestUpdateTrial(path, digest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := nextGuestUpdateBinary(path); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(dir, fmt.Sprintf("update-%x", digest))
	probe := func(err error) func(context.Context) error {
		return func(context.Context) error { return err }
	}
	failed := errors.New("fixture rejected authentication")
	if err := confirmRunningGuestTrial(t.Context(), dir, filepath.Join(dir, "other-agent"), func(context.Context) error {
		t.Error("a different executable attempted the trial poll")
		return nil
	}); !errors.Is(err, errGuestUpdateTrialConflict) {
		t.Errorf("trial from a different executable = %v, want conflict", err)
	}
	if err := confirmRunningGuestTrial(t.Context(), dir, candidate, probe(failed)); !errors.Is(err, failed) {
		t.Errorf("trial after rejected poll = %v, want poll failure", err)
	}
	if trial, err := loadGuestUpdateTrial(path); err != nil || trial.Phase != "testing" {
		t.Fatalf("trial after failed poll = %+v (%v), want testing", trial, err)
	}
	if err := confirmRunningGuestTrial(t.Context(), dir, candidate, probe(nil)); err != nil {
		t.Fatal(err)
	}
	if trial, err := loadGuestUpdateTrial(path); err != nil || trial.Phase != "confirmed" || trial.Active != digest {
		t.Errorf("trial after accepted poll = %+v (%v), want confirmed candidate", trial, err)
	}
}

// TestSelectGuestAgent verifies that the launcher rechecks a staged candidate
// and falls back immediately if the file changed or was replaced by a symlink.
func TestSelectGuestAgent(t *testing.T) {
	for _, mode := range []string{"verified", "changed", "missing", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			binary := []byte("fixture agent build")
			digest := sha256.Sum256(binary)
			trialPath := filepath.Join(dir, "update-trial.json")
			candidate := filepath.Join(dir, fmt.Sprintf("update-%x", digest))
			platform := filepath.Join(t.TempDir(), "platform-agent")
			switch mode {
			case "verified", "changed":
				body := binary
				if mode == "changed" {
					body = []byte("unexpected binary")
				}
				if err := os.WriteFile(candidate, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.WriteFile(platform, binary, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(platform, candidate); err != nil {
					t.Fatal(err)
				}
			}
			if err := requestGuestUpdateTrial(trialPath, digest, false); err != nil {
				t.Fatal(err)
			}
			want, phase := platform, "failed"
			if mode == "verified" {
				want, phase = candidate, "testing"
			}
			if got, err := SelectGuestAgent(dir, platform); err != nil || got != want {
				t.Fatalf("SelectGuestAgent(%s) = %q (%v), want %q", mode, got, err, want)
			}
			trial, err := loadGuestUpdateTrial(trialPath)
			if err != nil || trial.Phase != phase {
				t.Errorf("trial after %s = %s (%v), want %s", mode, trial.Phase, err, phase)
			}
			if mode == "verified" {
				if info, err := os.Stat(candidate); err != nil || info.Mode().Perm() != 0700 {
					t.Errorf("verified candidate = %v (%v), want a private executable", info, err)
				}
				if got, err := SelectGuestAgent(dir, platform); err != nil || got != platform {
					t.Errorf("SelectGuestAgent after unconfirmed launch = %q (%v), want platform binary", got, err)
				}
			}
		})
	}
}
