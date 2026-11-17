package guest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestProcessWatchStat refuses malformed proc records, excludes zombies, and
// binds a watch to Linux start ticks even when a PID is reused or its name changes.
func TestProcessWatchStat(t *testing.T) {
	first := processStatFixture("S", "123456")
	identity, alive, err := parseProcessStat(first)
	if err != nil || !alive || identity != "123456" {
		t.Fatalf("parseProcessStat(sleeping) = %q, %t, %v; want 123456, true, nil", identity, alive, err)
	}
	for _, state := range []string{"Z", "X", "x"} {
		_, alive, err := parseProcessStat(processStatFixture(state, "123456"))
		if err != nil || alive {
			t.Errorf("parseProcessStat(%s) = alive %t, error %v; want false, nil", state, alive, err)
		}
	}
	reused, alive, err := parseProcessStat(processStatFixture("S", "123457"))
	if err != nil || !alive || reused == identity {
		t.Errorf("parseProcessStat(reused PID) = %q, %t, %v; want different identity and alive", reused, alive, err)
	}
	for _, invalid := range []string{"", "123 (bad) S", processStatFixture("S", "bad")} {
		if _, _, err := parseProcessStat(invalid); err == nil {
			t.Errorf("parseProcessStat(%q) succeeded, want malformed record error", invalid)
		}
	}
}

func processStatFixture(state, start string) string {
	return "123 (sleep process ) with parentheses) " + state + " " + strings.Repeat("0 ", 18) + start + " 0 0\n"
}

// TestProcessWatchLeaseExpiry proves that a killed watch cannot keep renewing
// activity indefinitely and deleting a lease immediately ends its contribution.
func TestProcessWatchLeaseExpiry(t *testing.T) {
	now := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	registry := newProcessWatches(func() time.Time { return now })
	const token = "0123456789abcdef0123456789abcdef"
	registry.renew(token)
	if !registry.sample(now).Active {
		t.Error("process watch sample after heartbeat = idle, want active")
	}
	now = now.Add(processWatchLease)
	if registry.sample(now).Active {
		t.Error("process watch sample after lease expiry = active, want idle")
	}
	registry.renew(token)
	registry.remove(token)
	if registry.sample(now).Active {
		t.Error("process watch sample after removal = active, want idle")
	}
}

// TestWatchProcessCancellationAndExit drives the real local Unix transport and
// ensures a sleeping original process keeps alive until exit, reuse, or cancellation.
func TestWatchProcessCancellationAndExit(t *testing.T) {
	for _, end := range []string{"exit", "reuse", "zombie", "cancel"} {
		t.Run(end, func(t *testing.T) {
			registry := newProcessWatches(time.Now)
			socket := watchTestSocket(t)
			stop, err := listenProcessWatchSocket(t.Context(), socket, registry)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(stop)
			var state atomic.Int32
			inspect := func(int) (string, bool, error) {
				switch state.Load() {
				case 1:
					return "", false, os.ErrNotExist
				case 2:
					return "reused", true, nil
				case 3:
					return "original", false, nil
				default:
					return "original", true, nil
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- watchProcess(ctx, socket, 123, inspect, time.Millisecond) }()
			deadline := time.Now().Add(2 * time.Second)
			for !registry.sample(time.Now()).Active && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !registry.sample(time.Now()).Active {
				t.Fatal("watch did not establish its local lease")
			}
			switch end {
			case "exit":
				state.Store(1)
			case "reuse":
				state.Store(2)
			case "zombie":
				state.Store(3)
			case "cancel":
				cancel()
			}
			select {
			case err := <-done:
				if err != nil && !(end == "cancel" && errors.Is(err, context.Canceled)) {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("watch did not return after process end or cancellation")
			}
			if registry.sample(time.Now()).Active {
				t.Error("watch left an active lease after returning")
			}
		})
	}
}

// TestWatchRealSleepingProcess verifies Linux process identity through procfs;
// the sleep remains active without CPU use, and termination ends its watch.
func TestWatchRealSleepingProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("guest process identity uses Linux procfs")
	}
	command := exec.Command("sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	identity, alive, err := inspectProcess(command.Process.Pid)
	if err != nil || !alive || identity == "" {
		t.Fatalf("inspectProcess(sleep %d) = %q, %t, %v", command.Process.Pid, identity, alive, err)
	}
	registry := newProcessWatches(time.Now)
	socket := watchTestSocket(t)
	stop, err := listenProcessWatchSocket(t.Context(), socket, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	done := make(chan error, 1)
	go func() {
		done <- watchProcess(t.Context(), socket, command.Process.Pid, inspectProcess, time.Millisecond)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !registry.sample(time.Now()).Active && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !registry.sample(time.Now()).Active {
		t.Fatal("sleeping process did not keep the workspace alive")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Leave the process unreaped: zombies must end the watch as promptly as absent PIDs.
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch remained active after process became a zombie")
	}
	if registry.sample(time.Now()).Active {
		t.Error("exited process retained a keep-alive lease")
	}
}

// TestProcessWatchSocketValidation rejects malformed lease identifiers and
// occupied non-socket paths instead of accepting arbitrary local requests.
func TestProcessWatchSocketValidation(t *testing.T) {
	registry := newProcessWatches(time.Now)
	socket := watchTestSocket(t)
	if err := os.WriteFile(socket, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if stop, err := listenProcessWatchSocket(t.Context(), socket, registry); err == nil {
		stop()
		t.Error("watch socket overwrote an occupied file")
	}
	request, err := http.NewRequest(http.MethodPut, "http://local/watch/not-a-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	registry.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Errorf("invalid watch token = status %d, want 400", response.Code)
	}
}

func watchTestSocket(t *testing.T) string {
	t.Helper()
	// macOS Unix socket paths cannot accommodate t.TempDir's long test name.
	directory, err := os.MkdirTemp("/tmp", "watch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(directory, "activity.sock")
}
