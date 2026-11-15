//go:build utm_release

package release_test

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestUTMNativeTunnelRevocation verifies that an offboarding request closes a
// working native guest stream before the provider stops the same guest.
func TestUTMNativeTunnelRevocation(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_TUNNEL_REVOCATION") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_TUNNEL_REVOCATION=1 for native tunnel revocation")
	}
	runUTMGuestFixture(t, nil, nativeTunnelRevocation)
}

type nativeTunnelProvider struct {
	provider.Provider
	allowStop <-chan struct{}
}

// Stop injects an uncertain provider response until the native guest has had
// time to reject the revoked connection. Inspection and cleanup use real UTM.
func (p nativeTunnelProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	select {
	case <-p.allowStop:
		return p.Provider.Stop(ctx, req)
	default:
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
}

func verifyNativeTunnelRevocation(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, before provider.Inventory, owner string, tunnels *proxy.Tunnels, fixture *nativeTunnelFixture,
) {
	t.Helper()
	ref := before.Ref
	open := func(ctx context.Context) (net.Conn, error) {
		return tunnels.Open(ctx, ref.WorkspaceID, ref.Generation, proxy.StreamHeader{
			ServiceKey: "preview-4173", RequestID: "native-preview-fixture", Protocol: "websocket",
		})
	}
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var stream net.Conn
	var reader *bufio.Reader
	for {
		conn, err := open(wait)
		if err == nil {
			buffer := bufio.NewReader(conn)
			if err = readNativePreviewFrame(conn, buffer); err == nil {
				stream, reader = conn, buffer
				break
			}
			conn.Close()
		}
		select {
		case <-wait.Done():
			t.Fatalf("the packaged guest never delivered the preview WebSocket frame: %v", err)
		case <-ticker.C:
		}
	}
	defer stream.Close()
	t.Log("received a WebSocket frame from the native preview through the packaged guest agent")

	stopID := disableEnrollmentFixtureOwner(t, db, journal, tunnels.Registry, owner, ref.WorkspaceID)
	if err := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := reader.ReadByte()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("reading the active stream after offboarding = %v, want immediate closure", err)
	}

	denial, cancelDenial := context.WithTimeout(ctx, 50*time.Second)
	defer cancelDenial()
	select {
	case <-fixture.denied:
	case <-denial.Done():
		t.Fatal("the running native agent did not report that the controller denied its identity")
	}
	if restarted, err := open(ctx); err == nil {
		restarted.Close()
		t.Fatal("the revoked native guest reopened a preview stream")
	}
	running, err := machines.Inspect(ctx, ref)
	if err != nil || running.Ref != ref || running.Power != provider.Running {
		t.Fatalf("native VM after stream closure = %s (%v), want the same VM still running", running.Power, err)
	}
	t.Log("offboarding closed the live stream and refused fresh access while the original VM was still running")

	close(fixture.allowStop)
	waitNativeOperation(t, ctx, journal, stopID, "succeeded")
	stopped, err := machines.Inspect(ctx, ref)
	if err != nil || stopped.Ref != ref || stopped.Power != provider.Stopped {
		t.Fatalf("native VM after offboarding = %s (%v), want stopped", stopped.Power, err)
	}
	verifyOwnedDisk(t, before, stopped)
}

func readNativePreviewFrame(conn net.Conn, reader *bufio.Reader) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	const request = "GET /ws HTTP/1.1\r\nHost: preview.fixture\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return err
	}
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols ||
		response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		return fmt.Errorf("preview upgrade returned status %d", response.StatusCode)
	}
	frame := make([]byte, 7)
	if _, err := io.ReadFull(reader, frame); err != nil {
		return err
	}
	if string(frame) != "\x81\x05ready" {
		return errors.New("preview returned an unexpected WebSocket frame")
	}
	return conn.SetDeadline(time.Time{})
}

type nativeTunnelFixture struct {
	seed      string
	denied    chan struct{}
	allowStop chan struct{}
}

// startNativeTunnelFixture prepares only the localhost preview in the native
// guest. The packaged agent discovers the port and owns the TLS tunnel.
func startNativeTunnelFixture(t *testing.T, host string) *nativeTunnelFixture {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	path := "/" + id + "/"
	fixture := &nativeTunnelFixture{
		seed: "http://" + listener.Addr().String() + path, denied: make(chan struct{}, 1), allowStop: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path+"meta-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "instance-id: %s\n", id)
	})
	mux.HandleFunc("GET "+path+"user-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "#!/bin/sh\nset -eu\nbase='%s'\n%s", fixture.seed, nativePreviewScript)
	})
	mux.HandleFunc("GET "+path+"denied", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case fixture.denied <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("native preview seed server: %v", err)
		}
	})
	return fixture
}

const nativePreviewScript = `
python3 - <<'PY' &
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Preview(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if self.path != "/ws" or self.headers.get("Upgrade") != "websocket":
            self.send_error(400)
            return
        self.send_response(101)
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=")
        self.end_headers()
        self.wfile.write(b"\x81\x05ready")
        self.wfile.flush()
        self.rfile.read(1)
        self.close_connection = True

ThreadingHTTPServer(("127.0.0.1", 4173), Preview).serve_forever()
PY
preview_pid=$!
trap 'kill "$preview_pid" 2>/dev/null || true' EXIT
for attempt in $(seq 1 150); do
    if journalctl -b -u shiftbox-agent.service --quiet -o cat | grep -qx 'guest identity is not authorized'; then
        curl -fsS --retry 3 --max-time 3 "${base}denied"
        wait "$preview_pid"
        exit
    fi
    sleep 1
done
exit 1
`
