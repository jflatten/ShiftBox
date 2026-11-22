package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// TestSSHProxyPinsBeforeRelay verifies authenticated readiness precedes raw SSH
// bytes and that progress is written to stderr.
func TestSSHProxyPinsBeforeRelay(t *testing.T) {
	const workspaceID = "bfa7840b-c317-4d68-bc74-b5f435370dd0"
	const credential = "scoped-credential"
	hostKey := testSSHHostKey(t)
	pinDir := t.TempDir()
	server := testSSHWebSocket(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/ssh/workspaces/"+workspaceID+"/connect" {
			t.Errorf("request path = %q, want workspace %s", got, workspaceID)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
			t.Error("SSH upgrade did not carry the scoped bearer credential")
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
			t.Error("SSH upgrade sent browser authority")
		}
		writeSSHControl(t, ctx, conn, map[string]string{"type": "progress", "message": "Starting workspace"})
		writeSSHControl(t, ctx, conn, map[string]string{"type": "ready", "workspaceId": workspaceID, "hostPublicKey": hostKey})
		kind, data, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageBinary || string(data) != "client SSH input" {
			t.Errorf("SSH input = %q (%v, %v), want binary client input", data, kind, err)
			return
		}
		if err := conn.Write(ctx, websocket.MessageBinary, []byte("server SSH output")); err != nil {
			t.Error(err)
		}
	})
	hostAlias, pinPath, err := sshHostPin(server.URL, workspaceID, pinDir)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	checkOutput := writerFunc(func(p []byte) (int, error) {
		got, err := os.ReadFile(pinPath)
		if err != nil || string(got) != hostAlias+" "+hostKey+"\n" {
			t.Errorf("host pin at first SSH bytes = %q (%v), want controller key", got, err)
		}
		return stdout.Write(p)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = runSSHProxy(ctx, server.Client(), server.URL, workspaceID, credential, pinDir,
		strings.NewReader("client SSH input"), checkOutput, &stderr)
	if err != nil || stdout.String() != "server SSH output" || stderr.String() != "Starting workspace\n" {
		t.Errorf("SSH relay output = %q, diagnostic = %q, error = %v", &stdout, &stderr, err)
	}
}

// TestSSHProxyRejectsUnexpectedIdentity verifies refusal keeps an existing pin
// intact and never hands suspect server bytes to OpenSSH.
func TestSSHProxyRejectsUnexpectedIdentity(t *testing.T) {
	const workspaceID = "workspace-one"
	original, changed := testSSHHostKey(t), testSSHHostKey(t)
	for _, tc := range []struct {
		name, workspace, key, diagnostic string
	}{
		{"wrong workspace", "workspace-two", original, "workspace identity"},
		{"changed key", workspaceID, changed, "host key changed"},
		{"malformed key", workspaceID, original + " injected", "invalid workspace SSH host key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinDir := t.TempDir()
			server := testSSHWebSocket(t, func(ctx context.Context, conn *websocket.Conn, _ *http.Request) {
				writeSSHControl(t, ctx, conn, map[string]string{"type": "ready", "workspaceId": tc.workspace, "hostPublicKey": tc.key})
			})
			alias, path, err := sshHostPin(server.URL, workspaceID, pinDir)
			if err != nil {
				t.Fatal(err)
			}
			pin := alias + " " + original + "\n"
			if err := os.WriteFile(path, []byte(pin), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = runSSHProxy(ctx, server.Client(), server.URL, workspaceID, "scoped", pinDir,
				strings.NewReader("client bytes"), &stdout, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.diagnostic) || stdout.Len() != 0 {
				t.Errorf("runSSHProxy(%s) = %v, stdout = %q, want refusal before SSH transport", tc.name, err, &stdout)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != pin {
				t.Errorf("existing pin = %q (%v), want original pin", got, err)
			}
		})
	}
}

// TestSSHProxyDoesNotRedirect verifies the scoped bearer never follows a
// redirect, including one to a different endpoint on the same controller.
func TestSSHProxyDoesNotRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			t.Error("SSH upgrade followed a redirect")
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := runSSHProxy(ctx, server.Client(), server.URL, "workspace-one", "scoped", t.TempDir(),
		strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("runSSHProxy(redirect) = nil, want refusal")
	}
}

// TestSSHProxyStopsOnRemoteClose verifies a revoked connection can finish
// even when OpenSSH remains idle with its input pipe open.
func TestSSHProxyStopsOnRemoteClose(t *testing.T) {
	input, peer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); peer.Close() })
	hostKey := testSSHHostKey(t)
	server := testSSHWebSocket(t, func(ctx context.Context, conn *websocket.Conn, _ *http.Request) {
		writeSSHControl(t, ctx, conn, map[string]string{"type": "ready", "workspaceId": "workspace-one", "hostPublicKey": hostKey})
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = runSSHProxy(ctx, server.Client(), server.URL, "workspace-one", "scoped", t.TempDir(),
		input, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("runSSHProxy(remote close and idle input) = %v, want clean exit", err)
	}
	// The proxy must not close an inherited descriptor to break its read.
	if _, err := peer.Write([]byte("unused")); err != nil {
		t.Errorf("write to input pipe after remote close = %v, want descriptor intact", err)
	}
	if err := input.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("unused"))
	if _, err := io.ReadFull(input, got); err != nil || string(got) != "unused" {
		t.Errorf("unconsumed input after runSSHProxy = %q (%v), want unused", got, err)
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

func testSSHHostKey(t *testing.T) string {
	t.Helper()
	key, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}

func testSSHWebSocket(t *testing.T, handle func(context.Context, *websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"shiftbox.ssh.v1"}})
		if err != nil {
			t.Errorf("accept SSH websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		handle(ctx, conn, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeSSHControl(t *testing.T, ctx context.Context, conn *websocket.Conn, value map[string]string) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Error(err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Errorf("write SSH control: %v", err)
	}
}
