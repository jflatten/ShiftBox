package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"github.com/jflatten/ShiftBox/internal/workspacessh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	sshDialTimeout = 15 * time.Second
	sshWaitTimeout = 5 * time.Minute
)

var errSSHSignInRequired = errors.New("sign in to ShiftBox before connecting")

// runSSHProxy relays an authenticated workspace connection to OpenSSH. It never
// passes credentials or controller control messages over the SSH byte stream.
func runSSHProxy(
	ctx context.Context,
	client *http.Client,
	origin, workspaceID, credential, pinDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	origin, err := canonicalSSHOrigin(origin)
	if err != nil {
		return err
	}
	hostAlias, pinPath, err := sshHostPin(origin, workspaceID, pinDir)
	if err != nil {
		return err
	}
	if credential == "" {
		return errSSHSignInRequired
	}
	conn, err := dialWorkspaceSSH(ctx, client, origin, workspaceID, credential)
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	conn.SetReadLimit(4 << 10)
	readyCtx, cancel := context.WithTimeout(ctx, sshWaitTimeout)
	hostKey, err := readSSHReady(readyCtx, conn, workspaceID, stderr)
	cancel()
	if err != nil {
		return err
	}
	// OpenSSH waits for the guest handshake through the proxy, so the scoped
	// known_hosts file is in place before it receives the guest's key.
	if err := pinSSHHost(pinPath, hostAlias, hostKey); err != nil {
		return err
	}

	return relaySSH(ctx, conn, stdin, stdout)
}

func dialWorkspaceSSH(ctx context.Context, client *http.Client, origin, workspaceID, credential string) (*websocket.Conn, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// Only copy the transport and timeout. A browser cookie jar and redirect
	// policy do not belong to the workstation credential exchange.
	sshClient := &http.Client{
		Transport: client.Transport,
		Timeout:   client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx, cancel := context.WithTimeout(ctx, sshDialTimeout)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, origin+workspacessh.ConnectPath(workspaceID), &websocket.DialOptions{
		HTTPClient:   sshClient,
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + credential}},
		Subprotocols: []string{workspacessh.Subprotocol},
	})
	if err != nil {
		switch {
		case response == nil:
			return nil, fmt.Errorf("connect to workspace SSH: %w", err)
		case response.StatusCode == http.StatusUnauthorized:
			return nil, errSSHSignInRequired
		default:
			return nil, fmt.Errorf("controller declined workspace SSH (HTTP %d)", response.StatusCode)
		}
	}
	if conn.Subprotocol() != workspacessh.Subprotocol {
		_ = conn.CloseNow()
		return nil, errors.New("controller did not negotiate the workspace SSH protocol")
	}
	return conn, nil
}

func readSSHReady(ctx context.Context, conn *websocket.Conn, workspaceID string, stderr io.Writer) (string, error) {
	for {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			return "", fmt.Errorf("wait for workspace SSH: %w", err)
		}
		var control workspacessh.Control
		if kind != websocket.MessageText || json.Unmarshal(payload, &control) != nil {
			return "", errors.New("invalid workspace SSH readiness frame")
		}
		message := strings.Map(func(r rune) rune {
			if unicode.IsPrint(r) {
				return r
			}
			return -1
		}, control.Message)
		switch control.Type {
		case workspacessh.Progress:
			if _, err := fmt.Fprintln(stderr, message); err != nil {
				return "", fmt.Errorf("write SSH progress: %w", err)
			}
		case workspacessh.Error:
			if message == "" {
				return "", errors.New("controller could not open workspace SSH")
			}
			return "", fmt.Errorf("controller could not open workspace SSH: %s", message)
		case workspacessh.Ready:
			if control.WorkspaceID != workspaceID {
				return "", errors.New("workspace identity did not match the SSH request")
			}
			key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(control.HostPublicKey))
			if err != nil || key.Type() != ssh.KeyAlgoED25519 ||
				strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) != control.HostPublicKey {
				return "", errors.New("invalid workspace SSH host key")
			}
			return control.HostPublicKey, nil
		default:
			return "", errors.New("unknown workspace SSH control frame")
		}
	}
}

func relaySSH(ctx context.Context, conn *websocket.Conn, stdin io.Reader, stdout io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	conn.SetReadLimit(1 << 20)
	input := stdin
	if file, ok := stdin.(*os.File); ok {
		raw, err := file.SyscallConn()
		if err != nil {
			return fmt.Errorf("access OpenSSH input: %w", err)
		}
		pipe := sshProxyInput{ctx: ctx, file: file}
		if err := raw.Control(func(fd uintptr) { pipe.descriptor = int32(fd) }); err != nil {
			return fmt.Errorf("access OpenSSH input descriptor: %w", err)
		}
		input = pipe
	}
	sent := make(chan error, 1)
	go func() {
		_, err := io.Copy(stream, input)
		sent <- err
		if err != nil {
			cancel()
		}
	}()
	_, err := io.Copy(stdout, stream)
	cancel()
	sendErr := <-sent
	if err != nil {
		if sendErr != nil && !errors.Is(sendErr, net.ErrClosed) && !errors.Is(sendErr, context.Canceled) {
			return fmt.Errorf("send workspace SSH data: %w", sendErr)
		}
		return fmt.Errorf("receive workspace SSH data: %w", err)
	}
	return nil
}

// sshProxyInput waits for OpenSSH's inherited pipe without changing its file
// flags. Periodic cancellation checks let the relay join an idle reader when the
// controller ends the connection.
type sshProxyInput struct {
	ctx        context.Context
	file       *os.File
	descriptor int32
}

func (p sshProxyInput) Read(b []byte) (int, error) {
	descriptors := []unix.PollFd{{Fd: p.descriptor, Events: unix.POLLIN}}
	for p.ctx.Err() == nil {
		ready, err := unix.Poll(descriptors, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if ready > 0 && p.ctx.Err() == nil {
			return p.file.Read(b)
		}
	}
	return 0, p.ctx.Err()
}

// sshHostPin returns a stable HostKeyAlias and a private known_hosts file. The
// configuration generator and the proxy must use this same derivation.
func sshHostPin(origin, workspaceID, pinDir string) (hostAlias, path string, err error) {
	origin, err = canonicalSSHOrigin(origin)
	if err != nil {
		return "", "", err
	}
	if len(workspaceID) == 0 || len(workspaceID) > 128 ||
		strings.IndexFunc(workspaceID, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 {
		return "", "", errors.New("invalid workspace SSH identity")
	}
	if !filepath.IsAbs(pinDir) {
		return "", "", errors.New("workspace SSH pin directory must be absolute")
	}
	hash := sha256.Sum256([]byte(origin + "\x00" + workspaceID))
	hostAlias = "shiftbox-" + hex.EncodeToString(hash[:])
	return hostAlias, filepath.Join(pinDir, hostAlias+".known_hosts"), nil
}

func canonicalSSHOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("workspace SSH requires a valid HTTPS controller origin")
	}
	host := strings.ToLower(parsed.Host)
	if parsed.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host, nil
}

func pinSSHHost(path, alias, hostKey string) error {
	entry := alias + " " + hostKey + "\n"
	err := checkSSHHostPin(path, entry)
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create workspace SSH pin directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".pin-")
	if err != nil {
		return fmt.Errorf("create workspace SSH pin: %w", err)
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.WriteString(entry)
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return fmt.Errorf("write workspace SSH pin: %w", err)
	}
	// Publish atomically without replacing a pin that another SSH attempt has
	// just installed. Simultaneous first connections must agree on the key.
	err = os.Link(temp.Name(), path)
	if errors.Is(err, os.ErrExist) {
		return checkSSHHostPin(path, entry)
	}
	if err != nil {
		return fmt.Errorf("install workspace SSH pin: %w", err)
	}
	return nil
}

func checkSSHHostPin(path, entry string) error {
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read workspace SSH pin: %w", err)
	}
	if string(got) != entry {
		return errors.New("workspace SSH host key changed; connection refused")
	}
	return nil
}
