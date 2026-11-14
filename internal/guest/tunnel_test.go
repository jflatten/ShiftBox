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
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

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
