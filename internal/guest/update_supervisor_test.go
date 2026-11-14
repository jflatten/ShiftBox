package guest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
