package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestGuestShutdownReplay checks that a lost acknowledgement does not send
// another stop signal or remove the retained development container.
func TestGuestShutdownReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	const dispatch = `{"id":"0555292b-088e-4f5b-8ac6-8be6045ae630","version":7,"type":"stop",
		"command":"shutdown-for-stop","arguments":{}}`
	id := strings.Repeat("8", 64)
	var calls [][]string
	runtime := devContainer{run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{executable}, args...))
		if len(calls) == 1 {
			return []byte(id + "\n"), nil
		}
		return nil, nil
	}}
	for range 2 {
		journal, err := loadLocalJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyGuestPoll(t.Context(), journal, runtime, 4, "stopped", json.RawMessage(dispatch)); err != nil {
			t.Fatal(err)
		}
		report := journal.report()
		if report.Status != "succeeded" || report.Step != "workload.shutdown" ||
			report.Evidence.WaitForReached || report.Problem != "" {
			t.Errorf("shutdown report = %+v, want durable workload stop evidence", report)
		}
	}
	want := [][]string{
		{"/usr/bin/docker", "container", "ls", "--quiet", "--no-trunc", "--filter", "label=shiftbox.container=development"},
		{"/usr/bin/docker", "container", "stop", "--signal", "SIGTERM", "--timeout", "-1", "--", id},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("shutdown and replay invoked %q, want %q", calls, want)
	}
}

// TestGuestShutdownReconcilesInterruptedStop confirms a restarted agent
// inspects container state before trying a stop that lost its response.
func TestGuestShutdownReconcilesInterruptedStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	dispatch := ensureDispatch()
	dispatch.Command = "shutdown-for-stop"
	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.accept(dispatch); err != nil {
		t.Fatal(err)
	}
	interrupted := *journal.state.Operation
	interrupted.Phase = localRequired
	if err := journal.saveOperation(interrupted); err != nil {
		t.Fatal(err)
	}
	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runtime := devContainer{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[1] != "ls" {
			t.Fatal("shutdown repeated a stop after observing no running development container")
		}
		return nil, nil
	}}
	report, err := journal.shutdown(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "succeeded" || calls != 1 {
		t.Errorf("shutdown(interrupted effect) = (%+v, %v), calls %d, want success after one inspection", report, err, calls)
	}
}

// TestGuestShutdownRejectsAmbiguousInventory refuses to signal a workload
// when Docker does not identify exactly one running primary container.
func TestGuestShutdownRejectsAmbiguousInventory(t *testing.T) {
	journal, err := loadLocalJournal(filepath.Join(t.TempDir(), "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	dispatch.Command = "shutdown-for-stop"
	calls := 0
	runtime := devContainer{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[1] != "ls" {
			t.Fatal("shutdown sent a signal despite ambiguous Docker inventory")
		}
		return []byte(strings.Repeat("1", 64) + "\n" + strings.Repeat("2", 64) + "\n"), nil
	}}
	report, err := journal.shutdown(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "failed" || report.Problem != "guest.shutdown_failed" || calls != 1 {
		t.Errorf("shutdown(ambiguous inventory) = (%+v, %v), calls %d, want one inspection and a recorded failure", report, err, calls)
	}
}

// TestGuestPollSendsCommittedLifecycleEvidence tests the real HTTP exchange
// after reopening the durable journal written during an earlier poll.
func TestGuestPollSendsCommittedLifecycleEvidence(t *testing.T) {
	const response = `{"protocolVersion":"1","workspaceGeneration":4,"desiredState":"running",
		"serverTime":"2026-11-14T00:00:00Z","certificate":{"renewAfter":"2026-11-14T04:00:00Z"},
		"nextPollAfterSeconds":30,`
	const dispatch = `"operation":{"id":"0555292b-088e-4f5b-8ac6-8be6045ae630","version":7,
		"type":"provision","command":"ensure-development-container",
		"arguments":{"devcontainerPath":".devcontainer/devcontainer.json","retainedMounts":[]}}}`
	id := strings.Repeat("2", 64)
	requests := make(chan guestPollRequest, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input guestPollRequest
		if err := decodeAgentJSON(r.Body, &input); err != nil || r.URL.Path != pollPath || r.Method != http.MethodPost {
			t.Error("pollAgent() sent an unexpected HTTPS request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- input
		output := response + dispatch
		if input.Report != nil {
			output = response + `"operation":null}`
		}
		_, _ = io.WriteString(w, output)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "operation.json")
	runtime := devContainer{workspace: t.TempDir(), run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] != "up" {
			t.Errorf("pollAgent() ran %q, want up", args[0])
		}
		return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
	}}
	for range 2 {
		journal, err := loadLocalJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		input := newGuestPollRequest("1", "test", journal)
		if err := pollAgent(t.Context(), server.Client(), server.URL+pollPath, input, journal, runtime, nil); err != nil {
			t.Fatalf("pollAgent(%+v) = %v, want success", input, err)
		}
	}
	first, second := <-requests, <-requests
	if first.Report != nil {
		t.Errorf("first poll report = %+v, want no operation yet", first.Report)
	}
	if second.Report == nil || second.LastOperationVersion != 7 || second.LastOperationID != ensureDispatch().ID ||
		second.Report.Evidence.ContainerID != id || !second.Report.Evidence.WaitForReached {
		t.Errorf("second poll = %+v, want persisted waitFor evidence for operation version 7", second)
	}
}

// TestGuestPollDispatchUsesSnapshot uses the template's selected config path
// on both lifecycle stages and carries the committed result in the next poll.
func TestGuestPollDispatchUsesSnapshot(t *testing.T) {
	workspace := t.TempDir()
	journal, err := loadLocalJournal(filepath.Join(t.TempDir(), "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 64)
	var calls [][]string
	runtime := devContainer{workspace: workspace, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[0] == "up" {
			return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
		}
		return []byte(`{"outcome":"success"}`), nil
	}}
	const command = `{"id":"0555292b-088e-4f5b-8ac6-8be6045ae630","version":7,"type":"provision",
		"command":"ensure-development-container","arguments":{"repositoryUrl":"https://github.com/example/repo",
		"repositoryRef":"main","devcontainerPath":".devcontainer/custom/devcontainer.json","retainedMounts":[]}}`
	for range 2 {
		if err := applyGuestPoll(t.Context(), journal, runtime, 4, "running", json.RawMessage(command)); err != nil {
			t.Fatal(err)
		}
	}
	input := newGuestPollRequest("1", "test", journal)
	if input.LastOperationID != ensureDispatch().ID || input.LastOperationVersion != 7 ||
		input.Report == nil || input.Report.Status != "succeeded" || input.Report.Evidence.ContainerID != id {
		t.Errorf("newGuestPollRequest(after setup) = %+v, want persisted success for version 7", input)
	}
	wantConfig := filepath.Join(workspace, ".devcontainer", "custom", "devcontainer.json")
	if len(calls) != 2 {
		t.Fatalf("applyGuestPoll() made %d CLI calls, want 2", len(calls))
	}
	for _, call := range calls {
		if !strings.Contains(strings.Join(call, " "), "--config "+wantConfig) {
			t.Errorf("CLI call %q, want explicit config %q", call, wantConfig)
		}
	}
}

// TestGuestPollDispatchCannotChangeConfig verifies that replayed dispatches
// cannot continue an existing container using another template configuration.
func TestGuestPollDispatchCannotChangeConfig(t *testing.T) {
	workspace := t.TempDir()
	journal, err := loadLocalJournal(filepath.Join(t.TempDir(), "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	dispatch.Config = ".devcontainer/one/devcontainer.json"
	if err := journal.accept(dispatch); err != nil {
		t.Fatal(err)
	}
	dispatch.Version++
	dispatch.Config = ".devcontainer/two/devcontainer.json"
	runtime := devContainer{workspace: workspace, run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("changed dispatch started the CLI")
		return nil, nil
	}}
	if _, err := journal.ensureDevelopment(t.Context(), dispatch, runtime); !errors.Is(err, errGuestDispatchConflict) {
		t.Errorf("ensureDevelopment(changed config) = %v, want a dispatch conflict", err)
	}
}

// TestGuestJournalResumesAfterRestart proves the waitFor evidence survives a
// lost acknowledgment and prevents the required hooks from running again.
func TestGuestJournalResumesAfterRestart(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "operation.json")
	id := strings.Repeat("d", 64)
	dispatch := ensureDispatch()
	var calls []string
	runtime := devContainer{workspace: directory, run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, args[0])
		if args[0] == "up" {
			return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
		}
		return []byte(`{"outcome":"success"}`), nil
	}}

	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Step != "devcontainer.wait_for" || report.Status != "running" ||
		report.Evidence.ContainerID != id || !report.Evidence.WaitForReached || report.Attempt != 1 {
		t.Fatalf("ensureDevelopment(first poll) = (%+v, %v), want persisted waitFor milestone in attempt 1", report, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("operation journal mode = %o, want 600", mode)
	}

	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved := journal.report(); saved != report {
		t.Fatalf("report() after reopen = %+v, want %+v", saved, report)
	}
	// The controller can advance its version after observing progress.
	dispatch.Version++
	report, err = journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "succeeded" || report.Step != "devcontainer.lifecycle" ||
		report.Attempt != 1 || !report.Evidence.WaitForReached {
		t.Fatalf("ensureDevelopment(resumed poll) = (%+v, %v), want setup completed in attempt 1", report, err)
	}
	replayed, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || replayed != report {
		t.Errorf("ensureDevelopment(replayed poll) = (%+v, %v), want (%+v, nil)", replayed, err, report)
	}
	if want := []string{"up", "run-user-commands"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("CLI calls after restart and replay = %v, want %v", calls, want)
	}
}

// TestGuestJournalFencesStaleWork rejects a stale controller version and a
// delayed dispatch from an earlier workspace generation.
func TestGuestJournalFencesStaleWork(t *testing.T) {
	journal, err := loadLocalJournal(filepath.Join(t.TempDir(), "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	if err := journal.accept(dispatch); err != nil {
		t.Fatal(err)
	}
	stale := dispatch
	stale.Version--
	if err := journal.accept(stale); !errors.Is(err, errGuestDispatchConflict) {
		t.Errorf("accept(older version) = %v, want a dispatch conflict", err)
	}
	next := dispatch
	next.Generation++
	if err := journal.advanceGeneration(next.Generation); err != nil {
		t.Fatal(err)
	}
	if got := journal.report(); got.ID != "" {
		t.Errorf("report() after generation advance = %+v, want no operation", got)
	}
	if err := journal.accept(dispatch); !errors.Is(err, errGuestDispatchConflict) {
		t.Errorf("accept(older generation) = %v, want a dispatch conflict", err)
	}
	if err := journal.accept(next); err != nil {
		t.Errorf("accept(new generation) = %v, want success", err)
	}
}

// TestGuestJournalInterruptedHook avoids trusting upstream lifecycle markers
// after a guest crash may have interrupted a hook that already wrote its marker.
func TestGuestJournalInterruptedHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime := devContainer{workspace: t.TempDir(), run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		cancel()
		return nil, ctx.Err()
	}}
	if _, err := journal.ensureDevelopment(ctx, dispatch, runtime); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensureDevelopment(cancelled hook) = %v, want context.Canceled", err)
	}
	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if request := newGuestPollRequest("1", "test", journal); request.Report != nil || request.LastOperationID != "" {
		t.Errorf("poll during interrupted hook = %+v, want server to redeliver before reporting the interrupted effect", request)
	}
	runtime.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("ensureDevelopment resumed uncertain hook without checking its outcome")
		return nil, nil
	}
	report, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "failed" || report.Problem != "guest.lifecycle_interrupted" ||
		report.Evidence.WaitForReached {
		t.Errorf("ensureDevelopment(interrupted hook) = (%+v, %v), want reported interruption and no waitFor evidence", report, err)
	}
}

// TestGuestJournalKeepsLaterHookFailure replays a redacted failure without
// changing the connection milestone the controller may already have observed.
func TestGuestJournalKeepsLaterHookFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	id := strings.Repeat("e", 64)
	calls := 0
	runtime := devContainer{workspace: t.TempDir(), run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[0] == "up" {
			return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
		}
		return nil, errors.New("secret credential printed by a later hook")
	}}
	if _, err := journal.ensureDevelopment(t.Context(), dispatch, runtime); err != nil {
		t.Fatal(err)
	}
	report, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "failed" || report.Step != "devcontainer.lifecycle" ||
		report.Problem != "guest.later_setup_failed" || !report.Evidence.WaitForReached {
		t.Fatalf("ensureDevelopment(later hook failure) = (%+v, %v), want a bounded failure with waitFor evidence", report, err)
	}
	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || replayed != report || calls != 2 {
		t.Errorf("ensureDevelopment(replayed failure) = (%+v, %v), calls %d, want (%+v, nil), calls 2", replayed, err, calls, report)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret credential") {
		t.Error("guest journal contains raw command failure, want a fixed problem code")
	}
}

func ensureDispatch() guestDispatch {
	return guestDispatch{ID: "0555292b-088e-4f5b-8ac6-8be6045ae630", Generation: 4, Version: 7, Command: "ensure-development-container"}
}

// TestDevContainerOutputLimit exercises stdout through an io.Copy fast path,
// as used by os/exec when capturing output from a child process.
func TestDevContainerOutputLimit(t *testing.T) {
	var output devContainerOutput
	source := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", devContainerOutputMax+1))}
	if _, err := io.Copy(&output, source); err == nil {
		t.Error("io.Copy(CLI stdout over limit) = nil, want an output limit error")
	}
	if got := output.buffer.Len(); got > devContainerOutputMax {
		t.Errorf("CLI stdout retained %d bytes, want at most %d", got, devContainerOutputMax)
	}
}

// TestDevContainerLifecycle runs the required and remaining hooks as separate
// steps so an operation can record the connection gate before later work.
func TestDevContainerLifecycle(t *testing.T) {
	id := strings.Repeat("a", 64)
	workspace := t.TempDir()
	var calls [][]string
	runtime := devContainer{
		workspace:   workspace,
		config:      ".devcontainer/go/devcontainer.json",
		credentials: true,
		ide:         true,
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{executable}, args...))
			if executable == "/usr/bin/docker" {
				return nil, nil
			}
			if args[0] == "up" {
				return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{"waitFor":"postStartCommand"}}`), nil
			}
			if args[0] == "exec" {
				return nil, nil
			}
			return []byte(`{"outcome":"success"}`), nil
		},
	}

	evidence, err := runtime.ensure(t.Context())
	want := containerConnection{ID: id, WaitFor: "postStartCommand"}
	if err != nil || evidence != want {
		t.Fatalf("ensure() = (%+v, %v), want (%+v, nil)", evidence, err, want)
	}
	if err := runtime.finishSetup(t.Context(), evidence); err != nil {
		t.Fatalf("finishSetup(%+v) = %v, want nil", evidence, err)
	}
	wantCalls := [][]string{
		{"/usr/local/bin/devcontainer", "up", "--workspace-folder", workspace, "--docker-path", "/usr/bin/docker",
			"--docker-compose-path", "/bin/false", "--log-format", "json", "--id-label", "shiftbox.container=development",
			"--mount", "type=bind,source=/run/shiftbox-ide,target=/run/shiftbox-ide",
			"--mount", "type=bind,source=/run/shiftbox-credentials,target=/run/shiftbox-credentials",
			"--mount", "type=bind,source=/run/shiftbox-credentials/shiftbox,target=/usr/local/bin/shiftbox",
			"--config", workspace + "/.devcontainer/go/devcontainer.json", "--skip-non-blocking-commands",
			"--skip-post-attach", "--include-merged-configuration"},
		{"/usr/bin/docker", "cp", "/usr/local/lib/shiftbox/code-server/.", id + ":/opt/shiftbox-code-server"},
		{"/usr/local/bin/devcontainer", "exec", "--workspace-folder", workspace, "--docker-path", "/usr/bin/docker",
			"--docker-compose-path", "/bin/false", "--log-format", "json", "--id-label", "shiftbox.container=development",
			"--config", workspace + "/.devcontainer/go/devcontainer.json", "--container-id", id,
			"/bin/sh", "-c", startCodeServer},
		{"/usr/local/bin/devcontainer", "run-user-commands", "--workspace-folder", workspace, "--docker-path", "/usr/bin/docker",
			"--docker-compose-path", "/bin/false", "--log-format", "json", "--id-label", "shiftbox.container=development",
			"--config", workspace + "/.devcontainer/go/devcontainer.json", "--container-id", id, "--skip-post-attach"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("ensure() followed by finishSetup() invoked %q, want %q", calls, wantCalls)
	}
}

// TestDevContainerIDEStartFailure keeps failed launches out of the proxy gate
// and leaves the underlying command output out of the operation evidence.
func TestDevContainerIDEStartFailure(t *testing.T) {
	id := strings.Repeat("a", 64)
	runtime := devContainer{workspace: t.TempDir(), ide: true, run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		if executable == "/usr/bin/docker" {
			return nil, nil
		}
		if args[0] == "exec" {
			return nil, errors.New("fixture command contains project data")
		}
		return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
	}}
	if connection, err := runtime.ensure(t.Context()); connection != (containerConnection{}) || err == nil || strings.Contains(err.Error(), "project data") {
		t.Fatalf("ensure after IDE failure = %+v, %v, want no connection and bounded error", connection, err)
	}
	journal, err := loadLocalJournal(filepath.Join(t.TempDir(), "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := journal.ensureDevelopment(t.Context(), ensureDispatch(), runtime)
	if err != nil || report.Status != "failed" || report.Problem != "guest.ide_start_failed" || report.Evidence.WaitForReached || !validConfigurationReport(report) {
		t.Fatalf("operation after IDE failure = %+v, %v, want a valid IDE startup failure", report, err)
	}
}

// TestDevContainerRejectsExternalConfiguration keeps controller-supplied config
// paths relative to the checked-out project.
func TestDevContainerRejectsExternalConfiguration(t *testing.T) {
	runtime := devContainer{
		workspace: t.TempDir(),
		config:    "../other/devcontainer.json",
		run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("ensure() invoked the CLI for an external configuration")
			return nil, nil
		},
	}
	if evidence, err := runtime.ensure(t.Context()); err == nil || evidence != (containerConnection{}) {
		t.Errorf("ensure(external config) = (%+v, %v), want zero evidence and an error", evidence, err)
	}
}

// TestDevContainerRejectsInvalidEvidence covers command and hook failures,
// malformed output, and unexpected IDs that must never be used as CLI arguments.
func TestDevContainerRejectsInvalidEvidence(t *testing.T) {
	id := strings.Repeat("b", 64)
	for _, test := range []struct {
		name   string
		output string
		err    error
	}{
		{name: "hook exit", err: errors.New("secret from command output")},
		{name: "failure result", output: `{"outcome":"error","message":"secret from command output"}`},
		{name: "external command argument", output: `{"outcome":"success","containerId":"--help","mergedConfiguration":{}}`},
		{name: "missing merged configuration", output: `{"outcome":"success","containerId":"` + id + `"}`},
		{name: "unknown wait stage", output: `{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{"waitFor":"postAttachCommand"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := devContainer{workspace: t.TempDir(), run: func(context.Context, string, ...string) ([]byte, error) {
				return []byte(test.output), test.err
			}}
			evidence, err := runtime.ensure(t.Context())
			if err == nil || evidence != (containerConnection{}) {
				t.Fatalf("ensure() = (%+v, %v), want zero evidence and an error", evidence, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("ensure() forwarded command output in error %q, want a fixed description", err)
			}
		})
	}

	runtime := devContainer{workspace: t.TempDir(), run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("finishSetup invoked the CLI with unverified container evidence")
		return nil, nil
	}}
	if err := runtime.finishSetup(t.Context(), containerConnection{ID: "--help"}); err == nil {
		t.Error("finishSetup(invalid container) = nil, want an error")
	}
}

// TestDevContainerLaterHookFailure keeps required hook evidence available
// while returning an independent failure from the remaining hooks.
func TestDevContainerLaterHookFailure(t *testing.T) {
	id := strings.Repeat("c", 64)
	runtime := devContainer{workspace: t.TempDir(), run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "up" {
			return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
		}
		return nil, errors.New("later hook failed")
	}}
	evidence, err := runtime.ensure(t.Context())
	want := containerConnection{ID: id, WaitFor: "updateContentCommand"}
	if err != nil || evidence != want {
		t.Fatalf("ensure() = (%+v, %v), want (%+v, nil)", evidence, err, want)
	}
	if err := runtime.finishSetup(t.Context(), evidence); err == nil {
		t.Error("finishSetup(required hooks passed, later hook failed) = nil, want an error")
	}
}

// TestGuestExecutorWithDevContainerCLI exercises a real prebuilt image and
// Dockerfile, installs a local Feature, and measures which hooks ran at waitFor.
// Run it with SHIFTBOX_TEST_DEVCONTAINER_CLI set to the pinned CLI executable
// and SHIFTBOX_TEST_DEVCONTAINER_IMAGE set to a preloaded glibc Linux image.
func TestGuestExecutorWithDevContainerCLI(t *testing.T) {
	cli := os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_CLI")
	if cli == "" {
		t.Skip("set SHIFTBOX_TEST_DEVCONTAINER_CLI to enable the disposable Docker fixture")
	}
	image := os.Getenv("SHIFTBOX_TEST_DEVCONTAINER_IMAGE")
	if !filepath.IsAbs(cli) || image == "" || strings.ContainsAny(image, " \t\r\n") {
		t.Fatal("set an absolute CLI path and a preloaded Docker image")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	cliVersion, err := exec.CommandContext(t.Context(), cli, "--version").Output()
	if err != nil || strings.TrimSpace(string(cliVersion)) != "0.90.0" {
		t.Fatalf("Dev Container CLI version = %q, %v, want 0.90.0", cliVersion, err)
	}
	if err := exec.CommandContext(t.Context(), docker, "image", "inspect", "--", image).Run(); err != nil {
		t.Fatalf("inspect preloaded image %q: %v", image, err)
	}

	for _, fixture := range []struct {
		name    string
		build   bool
		badHook bool
	}{
		{name: "prebuilt-image"},
		{name: "dockerfile", build: true},
		{name: "required-hook-failure", badHook: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			state := t.TempDir()
			project := filepath.Join(state, "project")
			configDir := filepath.Join(project, ".devcontainer")
			featureDir := filepath.Join(configDir, "local-feature")
			if err := os.MkdirAll(featureDir, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(featureDir, "devcontainer-feature.json"), `{"id":"local-feature","version":"1.0.0","name":"Fixture"}`, 0600)
			write(filepath.Join(featureDir, "install.sh"), "#!/bin/sh\nset -eu\ntouch /usr/local/share/shiftbox-feature-installed\n", 0700)
			checkFeature := "test -e /usr/local/share/shiftbox-feature-installed"
			config := map[string]any{
				"image": image, "features": map[string]any{"./local-feature": map[string]any{}},
				"waitFor": "postCreateCommand", "initializeCommand": "echo initialized > lifecycle",
				"updateContentCommand": "echo updated >> lifecycle",
				"postCreateCommand":    "echo required >> lifecycle", "postStartCommand": "echo later >> lifecycle",
				"postAttachCommand": "echo attached >> lifecycle",
			}
			if fixture.build {
				delete(config, "image")
				config["build"] = map[string]string{"dockerfile": "Dockerfile"}
				write(filepath.Join(configDir, "Dockerfile"), "FROM "+image+"\nRUN touch /usr/local/share/shiftbox-dockerfile\n", 0600)
				checkFeature += " && test -e /usr/local/share/shiftbox-dockerfile"
			}
			config["onCreateCommand"] = checkFeature + " && echo created >> lifecycle"
			if fixture.badHook {
				config["postCreateCommand"] = "echo required >> lifecycle && false"
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(configDir, "devcontainer.json"), string(encoded), 0600)

			// Isolate Docker objects from workspace agents and parallel test runs.
			label := "shiftbox.executor-fixture=" + filepath.Base(state) + "-" + filepath.Base(filepath.Dir(state))
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 15*time.Second)
				defer cancel()
				ids, err := exec.CommandContext(cleanupCtx, docker, "ps", "-aq", "--filter", "label="+label).Output()
				if err == nil && len(strings.Fields(string(ids))) != 0 {
					err = exec.CommandContext(cleanupCtx, docker, append([]string{"rm", "--force"}, strings.Fields(string(ids))...)...).Run()
				}
				if err != nil {
					t.Errorf("remove Docker fixture %q: %v", label, err)
				}
			})
			journalPath := filepath.Join(state, "operation.json")
			journal, err := loadLocalJournal(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			dispatch := ensureDispatch()
			dispatch.Config = ".devcontainer/devcontainer.json"
			dispatch.Repository = "https://github.com/example/fixture.git"
			// Native Git checkout and authorization are covered in repository_test.go.
			// Seed the receipt so the real executor uses its production split between
			// bringing up the container and running the remote lifecycle commands.
			receipt := repositoryReceipt(project)
			if err := os.Mkdir(filepath.Dir(receipt), 0700); err != nil {
				t.Fatal(err)
			}
			prepared, err := json.Marshal(repositoryEvidence{
				repositorySource: repositorySource{URL: dispatch.Repository, Config: dispatch.Config},
				Path:             project,
				Commit:           strings.Repeat("a", 40),
			})
			if err != nil {
				t.Fatal(err)
			}
			write(receipt, string(prepared), 0600)
			runtime := devContainer{
				workspace:       project,
				config:          dispatch.Config,
				repository:      &repositoryPreparation{project: project},
				setProjectOwner: func(context.Context, string, int, int) error { return nil },
				run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
					if executable == "/usr/bin/docker" {
						return runDevContainerCLI(ctx, docker, args...)
					}
					if executable != devContainerCLI {
						t.Fatalf("executor invoked %q, want Docker or the pinned Dev Container CLI", executable)
					}
					args = slices.Clone(args)
					dockerArg, labelArg := slices.Index(args, "--docker-path"), slices.Index(args, "--id-label")
					if dockerArg < 0 || labelArg < 0 || dockerArg+1 == len(args) || labelArg+1 == len(args) {
						t.Fatalf("Dev Container CLI arguments = %q, want an explicit Docker path and workspace label", args)
					}
					args[dockerArg+1], args[labelArg+1] = docker, label
					return runDevContainerCLI(ctx, cli, args...)
				},
			}
			checkHooks := func(want string) {
				t.Helper()
				got, err := os.ReadFile(filepath.Join(project, "lifecycle"))
				if err != nil || string(got) != want {
					t.Errorf("lifecycle file = %q, %v, want %q", got, err, want)
				}
			}
			const requiredHooks = "initialized\ncreated\nupdated\nrequired\n"
			report, err := journal.ensureDevelopment(ctx, dispatch, runtime)
			if fixture.badHook {
				if err != nil || report.Status != "failed" || report.Problem != problemRequired || report.Evidence.WaitForReached {
					t.Errorf("ensureDevelopment(required hook failure) = %+v, %v, want bounded failure without waitFor", report, err)
				}
				checkHooks(requiredHooks)
				return
			}
			if err != nil || report.Status != "running" || !report.Evidence.WaitForReached || journal.state.Operation.Connection.WaitFor != "postCreateCommand" {
				t.Fatalf("ensureDevelopment(first dispatch) = %+v, %v, want waitFor evidence", report, err)
			}
			checkHooks(requiredHooks)

			journal, err = loadLocalJournal(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			resumed, err := journal.ensureDevelopment(ctx, dispatch, runtime)
			if err != nil || resumed.Status != "succeeded" || resumed.Attempt != 1 || resumed.Evidence != report.Evidence {
				t.Errorf("ensureDevelopment(after restart) = %+v, %v, want success in the same attempt and container", resumed, err)
			}
			checkHooks(requiredHooks + "later\n")
		})
	}
}
