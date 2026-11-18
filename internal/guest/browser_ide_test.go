package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		case "container":
			return []byte("[]"), nil
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
		if args[0] == "container" {
			return []byte("[]"), nil
		}
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
			if args[0] == "container" {
				return []byte("[]"), nil
			}
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

// TestBrowserIDEMountedBundle checks that a read-only bind starts the IDE
// directly, while a conflicting mount fails before the IDE can start.
func TestBrowserIDEMountedBundle(t *testing.T) {
	source := testCodeServerPackage(t)
	for _, tc := range []struct {
		name   string
		mount  dockerMount
		starts bool
	}{
		{name: "read only", mount: dockerMount{Type: "bind", Source: source, Destination: codeServerDir}, starts: true},
		{name: "writable", mount: dockerMount{Type: "bind", Source: source, Destination: codeServerDir, RW: true}},
		{name: "different source", mount: dockerMount{Type: "bind", Source: "/tmp/unrelated", Destination: codeServerDir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			mounts, err := json.Marshal([]dockerMount{tc.mount})
			if err != nil {
				t.Fatal(err)
			}
			var started bool
			var phases []string
			runtime := devContainer{
				workspace: filepath.Join(state, "project"), codeServerSource: source,
				timing: func(phase string, _ time.Duration, _ string) { phases = append(phases, phase) },
				run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
					switch args[0] {
					case "container":
						return mounts, nil
					case "exec":
						started = true
						return nil, nil
					default:
						t.Fatalf("startIDE(%s) invoked %s %q, want mount inspection and IDE startup only", tc.name, executable, args)
						return nil, nil
					}
				},
			}
			err = runtime.startIDE(t.Context(), strings.Repeat("a", 64))
			if started != tc.starts || (err == nil) != tc.starts {
				t.Errorf("startIDE(%s) started=%t, err=%v, want started=%t and success=%t", tc.name, started, err, tc.starts, tc.starts)
			}
			if _, err := os.Stat(filepath.Join(state, "code-server-install")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("startIDE(%s) legacy receipt stat error = %v, want os.ErrNotExist", tc.name, err)
			}
			if len(phases) == 0 || phases[0] != "ide_mount" {
				t.Errorf("startIDE(%s) phases = %v, want ide_mount first", tc.name, phases)
			}
		})
	}
}

// TestCodeServerDockerLauncher executes the generated shell logic and checks
// that only the development container receives a read-only bundle mount.
func TestCodeServerDockerLauncher(t *testing.T) {
	state := t.TempDir()
	source := filepath.Join(state, "developer's tools")
	runtime := devContainer{workspace: filepath.Join(state, "project"), codeServerSource: source}
	path, err := runtime.codeServerDocker()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(content), `exec /usr/bin/docker "$@"`, `printf '%s\n' "$@"`, 1)
	mount := "type=bind,source=" + source + ",target=" + codeServerDir + ",readonly"
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{
			args: []string{"run", "-l", developmentLabel, "workspace:latest"},
			want: []string{"run", "--mount", mount, "-l", developmentLabel, "workspace:latest"},
		},
		{
			args: []string{"run", "-l", "role=helper", "helper:latest"},
			want: []string{"run", "-l", "role=helper", "helper:latest"},
		},
		{
			args: []string{"buildx", "build", "--label", developmentLabel, "."},
			want: []string{"buildx", "build", "--label", developmentLabel, "."},
		},
	} {
		out, err := exec.CommandContext(t.Context(), "/bin/sh", append([]string{"-c", script, path}, tc.args...)...).Output()
		if err != nil {
			t.Fatalf("Docker launcher %q failed: %v", tc.args, err)
		}
		if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !slices.Equal(got, tc.want) {
			t.Errorf("Docker launcher %q forwarded %q, want %q", tc.args, got, tc.want)
		}
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

// TestIDEProbeSkipsAbsentSocket runs the startup probe with a missing socket
// and checks that it avoids spawning the bundled Node runtime.
func TestIDEProbeSkipsAbsentSocket(t *testing.T) {
	probe, _, ok := strings.Cut(startCodeServer, "\nif socket_ready; then")
	if !ok {
		t.Fatal("missing startup probe")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "node"), []byte("#!/bin/sh\ntouch \"$marker\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", probe+`
server=$1; socket=$1/missing; marker=$1/spawned; export marker; ! socket_ready`, "probe", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("absent socket probe: %s: %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "spawned")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("absent socket: Node marker stat = %v, want absent", err)
	}
}
