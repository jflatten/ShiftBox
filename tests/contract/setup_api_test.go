package contract_test

import (
	"crypto/tls"
	"github.com/jflatten/ShiftBox/internal/app"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBootstrapAccessBoundary prevents remote clients and DNS-rebound hosts from reaching protected setup, even with forged forwarding headers.
func TestBootstrapAccessBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, origin, host, remote string
		tls                        bool
		want                       int
	}{
		{"local IPv4", "https://127.0.0.1:8443", "127.0.0.1:8443", "127.0.0.1:32100", true, 204},
		{"local IPv6", "https://[::1]:8443", "[::1]:8443", "[::1]:32100", true, 204},
		{"remote peer", "https://127.0.0.1:8443", "127.0.0.1:8443", "192.0.2.4:32100", true, 403},
		{"DNS rebound host", "https://127.0.0.1:8443", "attacker.test:8443", "127.0.0.1:32100", true, 403},
		{"wrong port", "https://127.0.0.1:8443", "127.0.0.1:8444", "127.0.0.1:32100", true, 403},
		{"plaintext", "https://127.0.0.1:8443", "127.0.0.1:8443", "127.0.0.1:32100", false, 403},
		{"invalid peer", "https://127.0.0.1:8443", "127.0.0.1:8443", "unparseable", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler, err := app.ProtectBootstrap(tc.origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(204) }))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", tc.origin+"/api/v1/setup/status", nil)
			r.Host = tc.host
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Forwarded-Host", "127.0.0.1:8443")
			r.TLS = nil
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || reached != (tc.want == 204) {
				t.Errorf("bootstrap %s status/reached=%d/%t, want %d/%t", tc.name, w.Code, reached, tc.want, tc.want == 204)
			}
		})
	}
}

// TestBootstrapRejectsUnsafeOrigin prevents composition from accidentally configuring a public or plaintext setup address.
func TestBootstrapRejectsUnsafeOrigin(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8443", "https://0.0.0.0:8443", "https://192.0.2.1:8443", "https://localhost.attacker.test:8443", "https://127.0.0.1:8443/path", "https://127.0.0.1:8443?", "https://127.0.0.1:8443#", "https://127.0.0.1:65536"} {
		if _, err := app.ProtectBootstrap(origin, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
			t.Errorf("ProtectBootstrap(%q) accepted unsafe origin", origin)
		}
	}
}
