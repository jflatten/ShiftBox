package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestBrowserRouterIsolatesOrigins checks exact HTTPS host dispatch and
// prevents a service origin from exposing management handlers.
func TestBrowserRouterIsolatesOrigins(t *testing.T) {
	const management = "https://management.test:8443"
	const workspace = "00000000-0000-4000-8000-000000000004"
	hosts, err := config.NewServiceHosts(management, "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Fixture-Handler", name)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	router, err := web.NewBrowserRouter(hosts, marker("management"), marker("workspace"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target, handler string
		status          int
	}{
		{management + "/api/v1/session", "management", 204},
		{"https://" + workspace + ".ide.test:8443/api/v1/session", "workspace", 204},
		{"https://preview-8080--" + workspace + ".preview.test:8443/", "workspace", 204},
		{"https://management.test/api/v1/session", "", 421},
		{"https://" + workspace + ".ide.test:8444/", "", 421},
		{"https://other.test:8443/", "", 421},
		{"http://management.test:8443/", "", 421},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.target, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("X-Fixture-Handler") != tc.handler {
			t.Errorf("route %s = %d %q, want %d %q", tc.target, w.Code, w.Header().Get("X-Fixture-Handler"), tc.status, tc.handler)
		}
	}
}
