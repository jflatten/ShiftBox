package web_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestSessionTimeoutSettings protects the organization timeout and verifies
// that validation and concurrent edits preserve the last saved number and unit.
func TestSessionTimeoutSettings(t *testing.T) {
	db := keepAliveDatabase(t)
	session, principal, token := sessionHandlerFixture(t)
	principal.InstallationID = "00000000-0000-4000-8000-000000000001"
	read, update := web.SessionTimeoutHandlers(db, session.Origin, session.Now)
	lookup := func(context.Context, *sql.Tx, string) (auth.Principal, error) { return *principal, nil }
	read.Lookup, update.Lookup = lookup, lookup
	request := func(handler *web.AuthorizedHandler, body, version string, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(handler.Route.Method, session.Origin+"/api/v1/admin/settings/session", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", session.Origin)
		r.Header.Set("If-Match", version)
		if csrf {
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	const valid = `{"value":2,"unit":"hours"}`
	if got := request(&read, "", "", false); got.Code != 403 {
		t.Errorf("member read returned %d, want 403", got.Code)
	}
	if got := request(&update, valid, `"1"`, true); got.Code != 403 {
		t.Errorf("member update returned %d, want 403", got.Code)
	}
	principal.User.Role = "administrator"
	if got := request(&read, "", "", false); got.Code != 200 || got.Body.String() != "{\"value\":30,\"unit\":\"minutes\",\"version\":1}\n" {
		t.Fatalf("default read returned %d %s, want 30 minutes at version 1", got.Code, got.Body.String())
	}
	if got := request(&update, valid, `"1"`, false); got.Code != 403 {
		t.Errorf("administrator update without CSRF returned %d, want 403", got.Code)
	}
	for _, body := range []string{`{"value":0,"unit":"minutes"}`, `{"value":1.5,"unit":"hours"}`, `{"value":1,"unit":"weeks"}`, `{"value":401,"unit":"days"}`} {
		if got := request(&update, body, `"1"`, true); got.Code != 400 {
			t.Errorf("invalid update %s returned %d, want 400", body, got.Code)
		}
	}
	if got := request(&update, valid, `"1"`, true); got.Code != 204 {
		t.Fatalf("administrator update returned %d %s, want 204", got.Code, got.Body.String())
	}
	if got := request(&update, `{"value":1,"unit":"days"}`, `"1"`, true); got.Code != 412 {
		t.Errorf("stale update returned %d, want 412", got.Code)
	}
	if got := request(&read, "", "", false); got.Code != 200 || got.Body.String() != "{\"value\":2,\"unit\":\"hours\",\"version\":2}\n" {
		t.Errorf("reloaded setting returned %d %s, want 2 hours at version 2", got.Code, got.Body.String())
	}
}
