package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// TestWorkspaceSSHInternalCommand verifies the internal editor channel uses
// the existing guest binary's stdio without adding to its output.
func TestWorkspaceSSHInternalCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"ssh-forward", "127.0.0.1", "40123"}
	got := workspaceCommand(t.Context(), args, strings.NewReader("payload"), &stdout, &stderr, workspaceCommands{
		ssh: func(_ context.Context, forwarded []string, stdin io.Reader, stdout io.Writer) error {
			if !slices.Equal(forwarded, args) {
				t.Errorf("forwarded command = %q, want %q", forwarded, args)
			}
			_, err := io.Copy(stdout, stdin)
			return err
		},
	})
	if got != 0 || stdout.String() != "payload" || stderr.Len() != 0 {
		t.Errorf("workspaceCommand(ssh-forward) = %d, %q, %q, want 0, payload and empty stderr", got, stdout.String(), stderr.String())
	}
}

// TestWorkspaceHelpTriggers verifies that discovery never contacts a guest
// service and that unknown commands still list every supported command.
func TestWorkspaceHelpTriggers(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"help"}, 0}, {[]string{"--help"}, 0},
		{[]string{"unknown"}, 2}, {[]string{"secrets", "unknown"}, 2},
		{[]string{"secrets", "get", "--bogus"}, 2}, {[]string{"git", "credential", "bogus"}, 2},
	} {
		var stdout, stderr bytes.Buffer
		rejectLookup := func(context.Context, string) (string, error) {
			t.Error("help or invalid options attempted a credential lookup")
			return "", errors.New("unexpected lookup")
		}
		code := workspaceCommand(t.Context(), tc.args, strings.NewReader(""), &stdout, &stderr,
			workspaceCommands{secret: rejectLookup, git: rejectLookup})
		if code != tc.code {
			t.Errorf("workspaceCommand(%v) code = %d, want %d", tc.args, code, tc.code)
		}
		for _, command := range []string{"watch <PID>", "secrets list", "secrets get <key>", "secrets get-secret <key>", "git credential", "serve", "supervisor-select", "help"} {
			if !strings.Contains(stdout.String(), command) {
				t.Errorf("workspaceCommand(%v) help = %q, want command %q", tc.args, stdout.String(), command)
			}
		}
	}
}

// TestWorkspaceSecretCommands checks exact secret bytes and name/scope listing
// without leaking values or changing the existing get-secret compatibility form.
func TestWorkspaceSecretCommands(t *testing.T) {
	services := workspaceCommands{
		secret: func(context.Context, string) (string, error) { return "exact\nvalue", nil },
		keys: func(context.Context) ([]guest.SecretKey, error) {
			return []guest.SecretKey{{Name: "API_KEY", Scope: "user"}, {Name: "PROJECT_TOKEN", Scope: "project"}, {Name: "SHARED", Scope: "organization"}}, nil
		},
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"secrets", "get", "API_KEY"}, "exact\nvalue"},
		{[]string{"secrets", "get-secret", "API_KEY"}, "exact\nvalue"},
		{[]string{"secrets", "list"}, "API_KEY (user)\nPROJECT_TOKEN (project)\nSHARED (organization)\n"},
	} {
		var stdout, stderr bytes.Buffer
		code := workspaceCommand(t.Context(), tc.args, strings.NewReader(""), &stdout, &stderr, services)
		if code != 0 || stdout.String() != tc.want || stderr.Len() != 0 {
			t.Errorf("workspaceCommand(%v) = code %d, stdout %q, stderr %q; want 0, %q, empty", tc.args, code, stdout.String(), stderr.String(), tc.want)
		}
	}
}

// TestWorkspaceWatchCommand rejects invalid process identifiers and treats a
// user cancellation as a normal end to the foreground keep-alive command.
func TestWorkspaceWatchCommand(t *testing.T) {
	for _, pid := range []string{"0", "-1", "+1", "abc", "999999999999999999999"} {
		var stdout, stderr bytes.Buffer
		code := workspaceCommand(t.Context(), []string{"watch", pid}, strings.NewReader(""), &stdout, &stderr, workspaceCommands{})
		if code != 2 {
			t.Errorf("workspaceCommand(watch %q) = %d, want usage exit 2", pid, code)
		}
	}
	var stdout, stderr bytes.Buffer
	called := false
	code := workspaceCommand(t.Context(), []string{"watch", "123"}, strings.NewReader(""), &stdout, &stderr,
		workspaceCommands{watch: func(_ context.Context, pid int) error {
			called = true
			if pid != 123 {
				t.Errorf("watch PID = %d, want 123", pid)
			}
			return context.Canceled
		}})
	if !called || code != 0 || stderr.Len() != 0 {
		t.Errorf("workspaceCommand(watch 123) = called %t, code %d, stderr %q; want true, 0, empty", called, code, stderr.String())
	}
	stdout.Reset()
	code = workspaceCommand(t.Context(), []string{"watch", "123"}, strings.NewReader(""), &stdout, &stderr,
		workspaceCommands{watch: func(context.Context, int) error { return errors.New("socket unavailable") }})
	if code != 1 || !strings.Contains(stderr.String(), "Could not keep the workspace alive") {
		t.Errorf("workspaceCommand(watch offline) = %d, stderr %q; want 1 and recovery guidance", code, stderr.String())
	}
}
