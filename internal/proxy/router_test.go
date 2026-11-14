package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestTunnelProxyHTTP checks that each request opens a new scoped stream,
// removes controller authority, and preserves project paths and cookies.
func TestTunnelProxyHTTP(t *testing.T) {
	var opens atomic.Int32
	received := make(chan *http.Request, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Clone(r.Context())
		w.Header().Add("Set-Cookie", "app=value; Path=/")
		w.Header().Add("Set-Cookie", "__Host-shiftbox_origin=guest-value; Path=/; Secure")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	handler := tunnelProxy(func(ctx context.Context, workspace string, generation int64, header StreamHeader) (net.Conn, error) {
		opens.Add(1)
		if workspace != "workspace" || generation != 4 || header.ServiceKey != "ide" || header.Protocol != "http" || header.RequestID != "fixture" {
			t.Errorf("unexpected stream identity: %s %d %+v", workspace, generation, header)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	}, "workspace", 4, "ide", "fixture")
	for _, path := range []string{"/a%2Fb?param=1", "/second"} {
		request := httptest.NewRequest("GET", "https://ide.example.test"+path, nil)
		request.Header.Set("Cookie", "__Host-shiftbox_origin=secret; app=value")
		request.Header.Set("X-CSRF-Token", "secret")
		request.Header.Set("X-Forwarded-Host", "spoofed.example.test")
		request.Header.Set("X-ShiftBox-Origin-Grant", "secret")
		request.Trailer = http.Header{"X-Csrf-Token": {"secret"}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 204 || strings.Join(response.Header().Values("Set-Cookie"), ",") != "app=value; Path=/" {
			t.Fatalf("proxy response = %d, cookies %q, want 204 and only the application cookie", response.Code, response.Header().Values("Set-Cookie"))
		}
		select {
		case got := <-received:
			if got.RequestURI != path || got.Host != request.Host || got.Header.Get("Cookie") != "app=value" ||
				got.Header.Get("X-CSRF-Token") != "" || got.Header.Get("X-ShiftBox-Origin-Grant") != "" ||
				got.Header.Get("X-Forwarded-Host") != "" || len(got.Trailer) != 0 {
				t.Errorf("upstream request = %s %s with headers %v, want original path, host and sanitized headers", got.Host, got.RequestURI, got.Header)
			}
		default:
			t.Fatal("no upstream request arrived")
		}
	}
	if got := opens.Load(); got != 2 {
		t.Errorf("opened %d streams, want 2", got)
	}
}

// TestTunnelProxyWebSocket verifies exact browser origin checks and a real
// bidirectional upgrade while withholding the ShiftBox session cookie.
func TestTunnelProxyWebSocket(t *testing.T) {
	var opens atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Hop") != "" {
			t.Error("WebSocket upstream received an invalid upgrade or private headers")
		}
		conn, buffer, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		buffer.Flush()
		if line, err := buffer.ReadString('\n'); err == nil {
			fmt.Fprint(conn, line)
		}
	}))
	defer upstream.Close()
	forward := tunnelProxy(func(ctx context.Context, _ string, _ int64, header StreamHeader) (net.Conn, error) {
		opens.Add(1)
		if header.Protocol != "websocket" {
			t.Errorf("stream protocol = %q, want websocket", header.Protocol)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	}, "workspace", 4, "ide", "fixture")
	const host = "ide.example.test"
	bad := httptest.NewRequest("GET", "https://"+host+"/socket", nil)
	bad.Header.Set("Connection", "Upgrade")
	bad.Header.Set("Upgrade", "websocket")
	bad.Header.Set("Origin", "https://sibling.example.test")
	denied := httptest.NewRecorder()
	forward.ServeHTTP(denied, bad)
	if denied.Code != 403 || opens.Load() != 0 {
		t.Fatalf("foreign WebSocket returned %d and opened %d streams, want 403 and zero", denied.Code, opens.Load())
	}
	front := httptest.NewTLSServer(forward)
	defer front.Close()
	request, err := http.NewRequest("GET", front.URL+"/socket", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header.Set("Origin", "https://"+host)
	request.Header.Set("Connection", "Upgrade, X-Hop")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("X-Hop", "remove")
	request.Header.Set("Cookie", "__Host-shiftbox_origin=secret")
	response, err := front.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	channel, ok := response.Body.(interface{ Write([]byte) (int, error) })
	if response.StatusCode != 101 || !ok {
		t.Fatalf("upgrade returned %d with writable body %t, want 101 and a writable stream", response.StatusCode, ok)
	}
	if _, err := fmt.Fprint(channel, "fixture\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(response.Body).ReadString('\n'); line != "fixture\n" || err != nil {
		t.Errorf("upgraded stream echo = %q, %v, want fixture", line, err)
	}
}
