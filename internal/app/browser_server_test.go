package app_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestBrowserServerSeparatesManagementAndServices exercises real TLS and the
// same host routing built from the active installation settings.
func TestBrowserServerSeparatesManagementAndServices(t *testing.T) {
	// The fixture certificate only establishes transport. Preflight verifies
	// production hostname and wildcard coverage before calling this listener.
	cert := httptest.NewTLSServer(http.NotFoundHandler())
	pair := cert.TLS.Certificates[0]
	leaf := cert.Certificate()
	cert.Close()
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "browser.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state := &app.ControllerState{DB: db, BrowserTLS: pair, Installation: app.ActiveInstallation{
		Settings: config.InstallationSettings{ManagementOrigin: "https://management.test:8443",
			IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test"},
	}}
	registry := &proxy.Registry{}
	server, err := app.ListenBrowser("127.0.0.1:0", state, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Management", "yes")
		w.WriteHeader(http.StatusNoContent)
	}), &proxy.Tunnels{Registry: registry}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, tc := range []struct {
		host, marker string
		status       int
	}{
		{"management.test:8443", "yes", 204},
		{"00000000-0000-4000-8000-000000000004.ide.test:8443", "", 403},
		{"foreign.test:8443", "", 421},
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+server.Address+"/api/v1/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status || res.Header.Get("X-Management") != tc.marker || res.TLS.Version != tls.VersionTLS13 {
			t.Errorf("HTTPS for %s returned %d with management %q, want %d %q and TLS 1.3", tc.host,
				res.StatusCode, res.Header.Get("X-Management"), tc.status, tc.marker)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Serve(cancel) = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browser listener did not stop")
	}
}
