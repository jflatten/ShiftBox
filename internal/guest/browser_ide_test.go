package guest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBrowserIDERestart reuses a completed install in the same container and
// installs again when a rebuild creates a replacement container.
func TestBrowserIDERestart(t *testing.T) {
	id := strings.Repeat("a", 64)
	replacement := strings.Repeat("b", 64)
	var copies, starts int
	runtime := devContainer{workspace: filepath.Join(t.TempDir(), "project"), codeServerSource: testCodeServerPackage(t), run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		switch args[0] {
		case "cp":
			copies++
		case "exec":
			starts++
		default:
			t.Fatalf("unexpected IDE command: %s %q", executable, args)
		}
		return nil, nil
	}}
	for _, container := range []string{id, id, replacement} {
		if err := runtime.startIDE(t.Context(), container); err != nil {
			t.Fatalf("startIDE(%s) = %v, want success", container, err)
		}
	}
	if copies != 2 || starts != 3 {
		t.Errorf("startIDE(original, original, replacement) copied %d times and started %d times, want 2 copies and 3 starts", copies, starts)
	}
}

// TestBrowserIDERetriesInterruptedCopy ensures a partial upload is never
// reused and that installing and starting the IDE have independent deadlines.
func TestBrowserIDERetriesInterruptedCopy(t *testing.T) {
	id := strings.Repeat("a", 64)
	var copies, starts int
	var copyDeadline time.Time
	runtime := devContainer{workspace: filepath.Join(t.TempDir(), "project"), codeServerSource: testCodeServerPackage(t), run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("IDE command %s %q has no deadline", executable, args[0])
		}
		switch args[0] {
		case "cp":
			copies++
			copyDeadline = deadline
			if time.Until(deadline) <= 2*time.Minute {
				t.Error("IDE copy budget cannot accommodate an upload longer than two minutes")
			}
			if copies == 1 {
				return nil, context.DeadlineExceeded
			}
		case "exec":
			starts++
			if !deadline.Before(copyDeadline) || time.Until(deadline) < time.Minute {
				t.Error("IDE startup did not receive a fresh, separate startup budget")
			}
		default:
			t.Fatalf("unexpected IDE command: %s %q", executable, args)
		}
		return nil, nil
	}}
	if err := runtime.startIDE(t.Context(), id); !errors.Is(err, errBrowserIDE) || starts != 0 {
		t.Fatalf("startIDE(interrupted copy) = %v, starts %d, want IDE failure and zero starts", err, starts)
	}
	for range 2 {
		if err := runtime.startIDE(t.Context(), id); err != nil {
			t.Fatalf("startIDE(retry after interrupted copy) = %v, want success", err)
		}
	}
	if copies != 2 || starts != 2 {
		t.Errorf("startIDE(interrupted copy, retry, restart) copied %d times and started %d times, want 2 copies and 2 starts", copies, starts)
	}
}

// TestBrowserIDEPackageChange forces a reinstall for new package metadata and
// checks that an interrupted update invalidates the previous install's receipt.
func TestBrowserIDEPackageChange(t *testing.T) {
	id := strings.Repeat("a", 64)
	source := testCodeServerPackage(t)
	var copies int
	runtime := devContainer{workspace: filepath.Join(t.TempDir(), "project"), codeServerSource: source,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[0] == "cp" {
				copies++
				if copies == 2 {
					return nil, context.DeadlineExceeded
				}
			}
			return nil, nil
		},
	}
	if err := runtime.startIDE(t.Context(), id); err != nil {
		t.Fatalf("startIDE(initial package) = %v, want success", err)
	}
	path := filepath.Join(source, "package.json")
	if err := os.WriteFile(path, []byte(`{"version":"2.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.startIDE(t.Context(), id); !errors.Is(err, errBrowserIDE) || copies != 2 {
		t.Fatalf("startIDE(updated package) = %v with %d copies, want failed second copy", err, copies)
	}
	if err := os.WriteFile(path, []byte(`{"version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.startIDE(t.Context(), id); err != nil || copies != 3 {
		t.Errorf("startIDE(rolled back package) = %v with %d copies, want successful third copy", err, copies)
	}
}

func testCodeServerPackage(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"version":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return source
}
