package app_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
)

// TestBootstrapListenerTLSAndShutdown verifies actual loopback TLS, host isolation, protocol minimum, and joined shutdown.
func TestBootstrapListenerTLSAndShutdown(t *testing.T) {
	certificate, err := app.EnsureBootstrapCertificate(filepath.Join(t.TempDir(), "bootstrap.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.ListenBootstrap("127.0.0.1:0", certificate, func(origin string) (http.Handler, error) {
		return app.ProtectBootstrap(origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { defer close(done); done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("bootstrap server did not stop")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(certificate.Leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(server.Origin + "/api/v1/setup/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 || response.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("bootstrap response = %d / TLS %d", response.StatusCode, response.TLS.Version)
	}
	request, _ := http.NewRequest("GET", server.Origin, nil)
	request.Host = "attacker.test"
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("rebound host = %d, want 403", response.StatusCode)
	}
	oldTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MaxVersion: tls.VersionTLS12}}
	defer oldTransport.CloseIdleConnections()
	oldClient := &http.Client{Transport: oldTransport, Timeout: time.Second}
	if response, err := oldClient.Get(server.Origin); err == nil {
		response.Body.Close()
		t.Fatal("bootstrap accepted TLS 1.2")
	}
	plain := &http.Client{Timeout: time.Second}
	response, err = plain.Get(strings.Replace(server.Origin, "https://", "http://", 1))
	if err == nil {
		response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatal("bootstrap served plaintext HTTP")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Serve(cancel) = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap shutdown timed out")
	}
}

// TestBootstrapListenerRejectsPublicBind prevents composition from exposing temporary setup credentials on a network interface.
func TestBootstrapListenerRejectsPublicBind(t *testing.T) {
	for _, address := range []string{":8443", "0.0.0.0:8443", "[::]:8443", "192.0.2.1:8443", "localhost:8443"} {
		if server, err := app.ListenBootstrap(address, tls.Certificate{}, func(string) (http.Handler, error) {
			t.Error("unsafe listener reached handler construction")
			return http.NotFoundHandler(), nil
		}); err == nil {
			server.Close()
			t.Errorf("ListenBootstrap(%q) accepted unsafe bind", address)
		}
	}
}
