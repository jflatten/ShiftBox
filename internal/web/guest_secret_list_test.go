package web_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGuestSecretList requires the verified TLS peer and current authority,
// rejects caller-supplied identity hints, and returns names/scopes only.
func TestGuestSecretList(t *testing.T) {
	denied := false
	calls := 0
	leaf := &x509.Certificate{}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	handler := web.GuestSecretListHandler(func(_ context.Context, got *x509.Certificate, stamp time.Time) ([]secret.Key, error) {
		calls++
		if got != leaf || !stamp.Equal(now) {
			t.Error("listing did not use verified TLS peer and controller clock")
		}
		if denied {
			return nil, guest.ErrGuestUnauthorized
		}
		return []secret.Key{{Name: "TOKEN", Scope: "user"}}, nil
	}, func() time.Time { return now })
	request := func(target, body string, verified bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target, strings.NewReader(body))
		if verified {
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, VerifiedChains: [][]*x509.Certificate{{leaf}}}
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if got := request("https://controller.test/internal/v1/guest/secrets", "", false); got.Code != 403 || calls != 0 {
		t.Fatalf("unverified list=%d,calls%d,want403/no call", got.Code, calls)
	}
	for _, target := range []string{"https://controller.test/internal/v1/guest/secrets?workspace=other", "https://controller.test/internal/v1/guest/secrets?"} {
		if got := request(target, "", true); got.Code != 400 || calls != 0 {
			t.Errorf("identity hint list=%d,calls%d,want400/no call", got.Code, calls)
		}
	}
	if got := request("https://controller.test/internal/v1/guest/secrets", `{"name":"TOKEN"}`, true); got.Code != 400 || calls != 0 {
		t.Errorf("body list=%d,calls%d,want400/no call", got.Code, calls)
	}
	got := request("https://controller.test/internal/v1/guest/secrets", "", true)
	var payload struct {
		Keys []secret.Key `json:"keys"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &payload); err != nil || got.Code != 200 || len(payload.Keys) != 1 || payload.Keys[0].Name != "TOKEN" || strings.Contains(got.Body.String(), "value") || got.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("list=%d %s,%v,want names and scopes only/no-store", got.Code, got.Body.String(), err)
	}
	denied = true
	if got := request("https://controller.test/internal/v1/guest/secrets", "", true); got.Code != 403 || strings.Contains(got.Body.String(), "TOKEN") {
		t.Errorf("denied list=%d %s,want403/no keys", got.Code, got.Body.String())
	}
}
