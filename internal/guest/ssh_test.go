package guest

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestWorkspaceSSHCommandLifecycle verifies an authenticated tunnel stream
// runs the selected command, returns its exit status and closes on revocation.
func TestWorkspaceSSHCommandLifecycle(t *testing.T) {
	id := strings.Repeat("9", 64)
	runtime := devContainer{
		workspace: t.TempDir(),
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[1] == "ls" {
				return []byte(id), nil
			}
			return []byte("none"), nil
		},
		sshExec: func(ctx context.Context, args ...string) *exec.Cmd {
			if args[len(args)-1] == "long command" {
				return exec.CommandContext(ctx, "/bin/sleep", "60")
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'result'; exit 7")
		},
	}
	service, err := newWorkspaceSSH(t.TempDir(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(service.hostKey))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			service.serve(ctx, conn)
		}
		done <- err
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User: "untrusted-client-username", HostKeyCallback: ssh.FixedHostKey(key), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Output("fixture command")
	var exit *ssh.ExitError
	if string(output) != "result" || !errors.As(err, &exit) || exit.ExitStatus() != 7 {
		t.Fatalf("SSH exec = %q, %v, want exact output and status 7", output, err)
	}
	session, err = client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	// OpenSSH can force a PTY when local stdin has no terminal dimensions.
	if err := session.RequestPty("xterm-256color", 0, 0, nil); err != nil {
		t.Fatalf("SSH forced PTY without dimensions = %v, want default size", err)
	}
	output, err = session.CombinedOutput("fixture command")
	if string(output) != "result" || !errors.As(err, &exit) || exit.ExitStatus() != 7 {
		t.Fatalf("SSH PTY exec = %q, %v, want output and status 7", output, err)
	}
	if conn, err := client.Dial("tcp", "192.0.2.12:80"); err == nil {
		conn.Close()
		t.Fatal("SSH allowed a forward outside devcontainer loopback")
	}
	session, err = client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Start("long command"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH handler did not release its running command after cancellation")
	}
}

// TestWorkspaceSSHHostKeyPersists verifies ordinary agent restarts keep the
// host key and a damaged key is never silently replaced.
func TestWorkspaceSSHHostKeyPersists(t *testing.T) {
	state := t.TempDir()
	original, err := newWorkspaceSSH(state, devContainer{})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newWorkspaceSSH(state, devContainer{})
	if err != nil || original.hostKey != restarted.hostKey {
		t.Fatalf("newWorkspaceSSH after restart = %v, want the original key", err)
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(original.hostKey))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("SSH public key = %v, %v, want Ed25519", key, err)
	}
	keyPath := filepath.Join(state, "ssh-host-key")
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("SSH private key file = %v, %v, want mode 0600", info, err)
	}
	if err := os.WriteFile(keyPath, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newWorkspaceSSH(state, devContainer{}); err == nil {
		t.Fatal("newWorkspaceSSH with a damaged key succeeded, want failure")
	}
}

// TestWorkspaceSSHUsesPrimaryExecContext verifies every channel is tied to
// the fixed developer container and keeps binary output out of a PTY.
func TestWorkspaceSSHUsesPrimaryExecContext(t *testing.T) {
	id := strings.Repeat("a", 64)
	project := t.TempDir()
	runtime := devContainer{
		workspace: project,
		config:    "config/custom.json",
		run: func(_ context.Context, program string, args ...string) ([]byte, error) {
			inventory := []string{"container", "ls", "--quiet", "--no-trunc", "--filter", "label=" + developmentLabel}
			inspect := []string{"container", "inspect", "--format", "{{.HostConfig.NetworkMode}}", "--", id}
			switch {
			case program == "/usr/bin/docker" && slices.Equal(args, inventory):
				return []byte(id + "\n"), nil
			case program == "/usr/bin/docker" && slices.Equal(args, inspect):
				return []byte("none\n"), nil
			default:
				t.Errorf("SSH target called %s %q", program, args)
				return nil, os.ErrInvalid
			}
		},
		sshExec: func(ctx context.Context, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "fixture", args...)
		},
	}
	target, err := runtime.workspaceSSHTarget(t.Context())
	if err != nil || target.id != id || !target.forwarding {
		t.Fatalf("workspaceSSHTarget = %v, %v, want the primary container", target.id, err)
	}
	cmd := target.command(t.Context(), "", []string{workspaceSSHBinary, "ssh-sftp"})
	want := []string{
		"fixture", "exec", "--workspace-folder", project, "--docker-path", "/usr/bin/docker",
		"--docker-compose-path", "/bin/false", "--log-format", "text", "--id-label", developmentLabel,
		"--config", filepath.Join(project, "config/custom.json"), "--container-id", id, workspaceSSHBinary, "ssh-sftp",
	}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("SSH command = %q, want %q", cmd.Args, want)
	}
	runtime.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(id + "\n" + id + "\n"), nil
	}
	if _, err := runtime.workspaceSSHTarget(t.Context()); err == nil {
		t.Error("workspaceSSHTarget with ambiguous inventory succeeded, want failure")
	}
}

// TestWorkspaceSSHForwardingIsLocal verifies editor forwarding reaches only
// container loopback and cannot run with a shared network namespace.
func TestWorkspaceSSHForwardingIsLocal(t *testing.T) {
	for _, tc := range []struct {
		host string
		port uint32
		want string
	}{
		{host: "localhost", port: 40123, want: "127.0.0.1:40123"},
		{host: "::1", port: 40123, want: "[::1]:40123"},
		{host: "172.17.0.1", port: 40123},
		{host: "loopback.example", port: 40123},
		{host: "::", port: 40123},
		{host: "localhost", port: 0},
		{host: "localhost", port: 65536},
	} {
		if got := workspaceSSHForwardAddress(tc.host, tc.port); got != tc.want {
			t.Errorf("workspaceSSHForwardAddress(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
	for _, mode := range []string{"host", "container:other", ""} {
		if workspaceSSHForwarding(mode) {
			t.Errorf("workspaceSSHForwarding(%q) = true, want false", mode)
		}
	}
}

// TestWorkspaceSSHSessionCommands verifies the standard shell, exec, and
// bootstrap subsystem select fixed container commands.
func TestWorkspaceSSHSessionCommands(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		want    []string
	}{
		{name: "shell", want: []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}"`}},
		{name: "exec", payload: ssh.Marshal(struct{ Command string }{"exit 7"}), want: []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -c "$1"`, "shiftbox-ssh", "exit 7"}},
		{name: "subsystem", payload: ssh.Marshal(struct{ Name string }{"sftp"}), want: []string{workspaceSSHBinary, "ssh-sftp"}},
		{name: "subsystem", payload: ssh.Marshal(struct{ Name string }{"arbitrary"})},
		{name: "exec", payload: []byte("malformed")},
	} {
		if got := workspaceSSHSessionArgs(tc.name, tc.payload); !slices.Equal(got, tc.want) {
			t.Errorf("workspaceSSHSessionArgs(%q, %q) = %q, want %q", tc.name, tc.payload, got, tc.want)
		}
	}
}
