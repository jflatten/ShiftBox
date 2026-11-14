package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGitAuthorizationCallbackRedirect verifies that a successful state-bound
// callback needs no browser cookie and redirects without forwarding parameters.
func TestGitAuthorizationCallbackRedirect(t *testing.T) {
	const origin = "https://management.test"
	const path = origin + "/api/v1/git/authorization/callback"
	calls := 0
	handler := gitAuthorizationCallback(origin, func(_ context.Context, state, code, gotOrigin string) error {
		calls++
		if state != "fixture-state" || code != "fixture-code" || gotOrigin != origin {
			t.Error("callback did not pass the declared state, code, and configured origin")
		}
		return nil
	})
	for _, query := range []string{"?state=one&state=two&code=fixture-code", "?state=fixture-state&code=fixture-code&error=access_denied"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+query, nil))
		if response.Code != http.StatusBadRequest || calls != 0 {
			t.Errorf("callback with ambiguous parameters = %d, calls %d, want 400 and zero calls", response.Code, calls)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?state=fixture-state&code=fixture-code", nil))
	if response.Code != http.StatusSeeOther || calls != 1 || response.Header().Get("Location") != "/" ||
		response.Header().Get("Referrer-Policy") != "no-referrer" || response.Body.Len() != 0 {
		t.Errorf("callback success = %d, location %q, calls %d, want 303 to / with one exchange",
			response.Code, response.Header().Get("Location"), calls)
	}
}
