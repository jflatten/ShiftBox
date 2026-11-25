package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGitHubAppReturnPreservesStrictCookie uses the cross-site request shape:
// the management cookie is absent and the code reaches only a same-origin Integrations fragment.
func TestGitHubAppReturnPreservesStrictCookie(t *testing.T) {
	const origin = "https://shiftbox.test:8443"
	state := strings.Repeat("s", 43)
	r := httptest.NewRequest(http.MethodGet, origin+"/api/v1/admin/settings/github-app/return?code=fixture&state="+state, nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	GitHubAppReturn(origin).ServeHTTP(w, r)
	destination, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != 303 || destination.IsAbs() || destination.Path != "/integrations/" ||
		destination.RawQuery != "" || destination.Fragment != "github-code=fixture&github-state="+state ||
		w.Header().Get("Referrer-Policy") != "no-referrer" || w.Body.Len() != 0 {
		t.Fatalf("browser return = %d %q (%v), want a private Integrations fragment and empty response", w.Code, w.Header().Get("Location"), err)
	}
}
