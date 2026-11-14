package guest_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestGuestTunnelExpiry verifies an established tunnel closes its streams at
// the authentication deadline, even when the guest stays connected.
func TestGuestTunnelExpiry(t *testing.T) {
	now := time.Now()
	caPEM, keyPEM, key, issuer := authorityFixture(t)
	ca, err := guest.LoadCA(caPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", guest.ServerTLS(leafFixture(t, key, issuer, false), ca))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tunnels := &proxy.Tunnels{Registry: &proxy.Registry{}}
	controllerDone := make(chan error, 1)
	guestDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			deadline := time.Now().Add(time.Second)
			err = tunnels.Serve(ctx, conn.(*tls.Conn), "1", func(context.Context, *x509.Certificate) (proxy.TunnelAuthority, error) {
				return proxy.TunnelAuthority{WorkspaceID: "workspace", OwnerID: "owner", Generation: 1, ExpiresAt: deadline}, nil
			})
		}
		controllerDone <- err
	}()
	client := guest.ClientTLS(leafFixture(t, key, issuer, true), ca, "controller.test")
	go func() {
		guestDone <- guest.ServeTunnel(ctx, listener.Addr().String(), client, "1", 1,
			[]proxy.TunnelService{{Key: "ide", Target: upstream.Addr().String(), Protocols: []string{"websocket"}}})
	}()
	open := func() (net.Conn, error) {
		return tunnels.Open(ctx, "workspace", 1, proxy.StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "websocket"})
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var active net.Conn
	for {
		stream, err := open()
		if err == nil {
			active = stream
			break
		}
		select {
		case err := <-controllerDone:
			t.Fatalf("controller exited before the tunnel was usable: %v", err)
		case <-ctx.Done():
			t.Fatal("tunnel never became available")
		case <-ticker.C:
		}
	}
	defer active.Close()
	select {
	case err := <-controllerDone:
		if err == nil || !strings.Contains(err.Error(), "identity expired") {
			t.Fatalf("controller tunnel closed with %v, want identity expiry", err)
		}
	case <-ctx.Done():
		t.Fatal("controller did not close the expired tunnel")
	}
	select {
	case <-guestDone:
	case <-ctx.Done():
		t.Fatal("guest connection remained open after its authentication expired")
	}
	if err := active.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = active.Read(make([]byte, 1))
	if err == nil {
		t.Error("active stream remained readable after expiry")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Error("active stream remained open after expiry")
	}
	if stream, err := open(); err == nil {
		stream.Close()
		t.Error("expired authentication permitted a WebSocket stream")
	}
}

// TestGuestTunnelHTTP authenticates a guest over TLS, routes separate streams
// by service key, and closes the session when its registration is revoked.
func TestGuestTunnelHTTP(t *testing.T) {
	now := time.Now()
	certPEM, keyPEM, key, cert := authorityFixture(t)
	ca, err := guest.LoadCA(certPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Received-Path", r.URL.Path)
		w.Header().Set("X-Received-Cookie", r.Header.Get("Cookie"))
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch r.Host {
			case "starting.ide.test":
				_, _ = w.Write([]byte("<p>Starting workspace</p>"))
				return
			case "redirect.ide.test":
				http.Redirect(w, r, "https://workspace.ide.test/", http.StatusFound)
				return
			case "project.ide.test":
				if r.URL.Query().Get("folder") != "/workspaces/project" {
					http.Redirect(w, r, "./?folder=/workspaces/project", http.StatusFound)
					return
				}
			case "oversized.ide.test":
				_, _ = w.Write([]byte(`<meta id="vscode-workbench-web-configuration" data-settings="{}">` +
					strings.Repeat(" ", 512<<10)))
				return
			case "truncated.ide.test":
				w.Header().Set("Content-Length", "1024")
			}
			_, _ = w.Write([]byte(`<meta id="vscode-workbench-web-configuration" data-settings="{}">`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer local.Close()
	target, err := url.Parse(local.URL)
	if err != nil {
		t.Fatal(err)
	}
	server, err := tls.Listen("tcp", "127.0.0.1:0", guest.ServerTLS(leafFixture(t, key, cert, false), ca))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	registry := &proxy.Registry{}
	tunnels := &proxy.Tunnels{Registry: registry}
	done := make(chan error, 2)
	clientTLS := guest.ClientTLS(leafFixture(t, key, cert, true), ca, "controller.test")
	go func() {
		conn, err := server.Accept()
		if err == nil {
			err = tunnels.Serve(ctx, conn.(*tls.Conn), "1", func(_ context.Context, cert *x509.Certificate) (proxy.TunnelAuthority, error) {
				return proxy.TunnelAuthority{WorkspaceID: "workspace", OwnerID: "owner", Generation: 4, ExpiresAt: cert.NotAfter}, nil
			})
		}
		done <- err
	}()
	go func() {
		done <- guest.ServeTunnel(ctx, server.Addr().String(), clientTLS,
			"1", 4, []proxy.TunnelService{{Key: "ide", Target: target.Host, Protocols: []string{"http", "websocket"}}})
	}()
	open := func(key string, generation int64) (net.Conn, error) {
		return tunnels.Open(ctx, "workspace", generation, proxy.StreamHeader{ServiceKey: key, RequestID: "fixture", Protocol: "http"})
	}
	var stream net.Conn
	for stream == nil {
		stream, _ = open("ide", 4)
		if stream != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("tunnel never became ready")
		case err := <-done:
			t.Fatalf("tunnel ended before registration: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, path := range []string{"/first", "/second"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, local.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := req.Write(stream); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(stream), req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if got := response.Header.Get("X-Received-Path"); got != path {
			t.Errorf("upstream received %q, want %q", got, path)
		}
		stream.Close()
		if path == "/first" {
			stream, err = open("ide", 4)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	request := httptest.NewRequest(http.MethodGet, "https://workspace.ide.test/via-proxy", nil)
	request.Header.Set("Cookie", "__Host-shiftbox_origin=private; app=fixture")
	response := httptest.NewRecorder()
	tunnels.Forward(response, request, "workspace", 4, "ide", "proxy-fixture")
	if response.Code != http.StatusNoContent || response.Header().Get("X-Received-Path") != "/via-proxy" ||
		response.Header().Get("X-Received-Cookie") != "app=fixture" {
		t.Errorf("HTTP through Yamux returned status %d and headers %v, want 204, exact path, and application cookie",
			response.Code, response.Header())
	}
	if err := tunnels.ProbeIDE(ctx, "workspace", 4, "workspace.ide.test"); err != nil {
		t.Errorf("ProbeIDE(authenticated IDE service) = %v, want success", err)
	}
	if err := tunnels.ProbeIDE(ctx, "workspace", 4, "project.ide.test"); err != nil {
		t.Errorf("ProbeIDE(project folder redirect) = %v, want success", err)
	}
	if err := tunnels.ProbeIDE(ctx, "workspace", 5, "workspace.ide.test"); err == nil {
		t.Error("ProbeIDE(stale generation) = nil, want denial")
	}
	for _, host := range []string{"starting.ide.test", "redirect.ide.test", "oversized.ide.test", "truncated.ide.test"} {
		if err := tunnels.ProbeIDE(ctx, "workspace", 4, host); err == nil {
			t.Errorf("ProbeIDE(%s) = nil, want invalid workbench response", host)
		}
	}
	for _, attempt := range []struct {
		key        string
		generation int64
	}{{"management", 4}, {"ide", 5}} {
		if denied, err := open(attempt.key, attempt.generation); err == nil {
			denied.Close()
			t.Error("opened an unregistered service or a stale generation")
		}
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if conn, err := tunnels.Open(cancelled, "workspace", 4, proxy.StreamHeader{ServiceKey: "ide", RequestID: "cancelled", Protocol: "http"}); err == nil {
		conn.Close()
		t.Error("opened a stream for a cancelled browser request")
	}
	revokedStream, err := open("ide", 4)
	if err != nil {
		t.Fatalf("open after cancelled browser request: %v", err)
	}
	defer revokedStream.Close()
	if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.WorkspaceID == "workspace" }); err != nil {
		t.Fatal(err)
	}
	if _, err := revokedStream.Read(make([]byte, 1)); err == nil {
		t.Error("active stream remained readable after tunnel revocation")
	}
	if conn, err := open("ide", 4); err == nil {
		conn.Close()
		t.Error("revoked tunnel is still opening streams")
	}
	for range 2 {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("tunnel did not close after revocation")
		}
	}
}
