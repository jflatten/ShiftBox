package guest

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

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
		workspace: workspace,
		config:    ".devcontainer/go/devcontainer.json",
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{executable}, args...))
			if args[0] == "up" {
				return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{"waitFor":"postStartCommand"}}`), nil
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
			"--config", workspace + "/.devcontainer/go/devcontainer.json", "--skip-non-blocking-commands",
			"--skip-post-attach", "--include-merged-configuration"},
		{"/usr/local/bin/devcontainer", "run-user-commands", "--workspace-folder", workspace, "--docker-path", "/usr/bin/docker",
			"--docker-compose-path", "/bin/false", "--log-format", "json", "--id-label", "shiftbox.container=development",
			"--config", workspace + "/.devcontainer/go/devcontainer.json", "--container-id", id, "--skip-post-attach"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("ensure() followed by finishSetup() invoked %q, want %q", calls, wantCalls)
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
