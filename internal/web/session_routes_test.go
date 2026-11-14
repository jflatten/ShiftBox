package web_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	_ "modernc.org/sqlite"
)

// TestLogoutPersistsRevocationBeforeConnectionClose proves the endpoint invalidates stored authority before closing live access.
func TestLogoutPersistsRevocationBeforeConnectionClose(t *testing.T) {
	handler, principal, token := sessionHandlerFixture(t)
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	principal.User.PrimaryEmail = "member@example.test"
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := store.CreateUser(ctx, tx, principal.User, "setup", handler.Now()); err != nil {
			return err
		}
		return store.SaveSession(ctx, tx, principal.Session)
	}); err != nil {
		t.Fatal(err)
	}
	handler.Lookup = func(ctx context.Context, token string) (auth.Principal, error) {
		current := auth.Principal{InstallationState: "active"}
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			current.Session, err = store.Session(ctx, tx, token)
			if err != nil {
				return err
			}
			current.User, err = store.User(ctx, tx, current.Session.UserID)
			return err
		})
		return current, err
	}
	var registry proxy.Registry
	closed := false
	unregister, err := registry.Register(proxy.Scope{SessionID: principal.Session.ID}, func() error { return nil }, func() error {
		current, err := handler.Lookup(ctx, token)
		if err != nil {
			return err
		}
		if current.Session.RevokedAt == nil {
			return errors.New("connection closed before durable revocation")
		}
		closed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	handler.Revoke = func(ctx context.Context, id string) error {
		return store.RevokeAndClose(ctx, id, handler.Now(), "logout", &registry)
	}
	request := httptest.NewRequest(http.MethodDelete, "https://management.test/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	request.Header.Set("Origin", "https://management.test")
	request.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 204 || !closed {
		t.Fatalf("DELETE stored session status/closed = %d/%t, want 204/true", response.Code, closed)
	}
	request.Method = http.MethodGet
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Errorf("GET revoked stored session = %d, want 401", response.Code)
	}
}

// TestSessionReadUsesCurrentAuthority exposes current gates and CSRF without leaking the cookie or stored digests.
func TestSessionReadUsesCurrentAuthority(t *testing.T) {
	handler, principal, token := sessionHandlerFixture(t)
	request := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET session status/cache = %d/%q, want 200/no-store", response.Code, response.Header().Get("Cache-Control"))
	}
	var view struct {
		User        auth.User `json:"user"`
		CSRFToken   string    `json:"csrfToken"`
		Permissions []string  `json:"permissions"`
		Mode        string    `json:"mode"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.User.ID != principal.User.ID || view.CSRFToken != auth.CSRFToken(token) || view.Mode != "normal" || len(view.Permissions) == 0 {
		t.Error("GET session did not return current principal, mode, permissions and bound CSRF")
	}
	for _, secret := range []string{token, principal.Session.TokenDigest, principal.Session.CSRFDigest} {
		if strings.Contains(response.Body.String(), secret) {
			t.Error("GET session leaked stored credential material")
		}
	}
	principal.User.MembershipState = "disabled"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Errorf("GET disabled session = %d, want 401", response.Code)
	}
}

// TestLogoutRequiresOriginAndCSRF prevents a sibling preview from revoking a management session.
func TestLogoutRequiresOriginAndCSRF(t *testing.T) {
	for _, origin := range []string{"", "https://preview.management.test", "https://management.test"} {
		handler, _, token := sessionHandlerFixture(t)
		called := false
		handler.Revoke = func(context.Context, string) error { called = true; return nil }
		request := httptest.NewRequest(http.MethodDelete, "https://management.test/api/v1/session", nil)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		// The correct origin must also supply the session's CSRF token.
		if origin == "https://management.test" {
			request.Header.Set("X-CSRF-Token", "wrong")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 403 || called {
			t.Errorf("DELETE session origin %q = %d, revoke=%t, want 403/false", origin, response.Code, called)
		}
	}
}

// TestSessionLookupFailure distinguishes an unknown cookie from unavailable storage without leaking details.
func TestSessionLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{sql.ErrNoRows, 401}, {errors.New("private database location"), 500}} {
		handler, _, token := sessionHandlerFixture(t)
		handler.Lookup = func(context.Context, string) (auth.Principal, error) { return auth.Principal{}, tc.err }
		request := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/session", nil)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || strings.Contains(response.Body.String(), "private database") {
			t.Errorf("GET session lookup error status = %d, want %d with redacted error", response.Code, tc.status)
		}
	}
}

// TestSessionReadRestrictedPermissions retains the password gate and excludes resource-specific administrator access.
func TestSessionReadRestrictedPermissions(t *testing.T) {
	handler, principal, token := sessionHandlerFixture(t)
	principal.User.Role = "administrator"
	principal.User.MustChangePassword = true
	principal.Session.Mode = "password_change"
	request := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var view struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if strings.Join(view.Permissions, ",") != "session.view,session.logout,session.change_password" {
		t.Errorf("GET password-change permissions = %v, want session view/logout/change_password only", view.Permissions)
	}
}

// TestLogoutWaitsForRevocationAndClosure refuses success if durable revocation or connection closure fails.
func TestLogoutWaitsForRevocationAndClosure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		handler, principal, token := sessionHandlerFixture(t)
		called := false
		handler.Revoke = func(_ context.Context, id string) error {
			called = true
			if id != principal.Session.ID {
				t.Errorf("Revoke session ID = %q, want %q", id, principal.Session.ID)
			}
			if fail {
				return errors.New("private storage failure")
			}
			return nil
		}
		request := httptest.NewRequest(http.MethodDelete, "https://management.test/api/v1/session", nil)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		request.Header.Set("Origin", "https://management.test")
		request.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := 204
		if fail {
			want = 500
		}
		if response.Code != want || !called {
			t.Errorf("DELETE session failure=%t status/call = %d/%t, want %d/true", fail, response.Code, called, want)
		}
		if strings.Contains(response.Body.String(), "private storage") {
			t.Error("DELETE session leaked backend error")
		}
		if !fail && response.Result().Cookies()[0].MaxAge != -1 {
			t.Error("DELETE session did not clear cookie after revocation")
		}
	}
}

func sessionHandlerFixture(t *testing.T) (*web.SessionHandler, *auth.Principal, string) {
	t.Helper()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	session, token, err := auth.NewSession("session", "user", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	principal := &auth.Principal{User: auth.User{ID: "user", Role: "member", MembershipState: "active", CredentialMode: "local"}, Session: session, InstallationState: "active"}
	handler := &web.SessionHandler{Origin: "https://management.test", Now: func() time.Time { return now }, Lookup: func(_ context.Context, got string) (auth.Principal, error) {
		if got != token {
			return auth.Principal{}, errors.New("unknown cookie")
		}
		return *principal, nil
	}}
	return handler, principal, token
}

// TestManagementCookieBoundary keeps management credentials host-only and inaccessible to scripts.
func TestManagementCookieBoundary(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	session, token, err := auth.NewSession("session", "user", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := web.SetSessionCookie(response, token, session.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("SetSessionCookie cookie count = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-shiftbox_session" || cookie.Value != token || cookie.Domain != "" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || !cookie.Expires.Equal(session.ExpiresAt) {
		t.Error("SetSessionCookie did not preserve the management cookie boundary")
	}
	request := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/session", nil)
	request.AddCookie(cookie)
	got, err := web.SessionToken(request)
	if err != nil || got != token {
		t.Errorf("SessionToken(issued cookie) matched = %t, error = %v, want true, nil", got == token, err)
	}
}

// TestSessionCookieRejectsAmbiguity prevents missing, malformed, or duplicate credentials from selecting authority.
func TestSessionCookieRejectsAmbiguity(t *testing.T) {
	valid := strings.Repeat("A", 43)
	for _, header := range []string{"", "shiftbox_session=" + valid, "__Host-shiftbox_session=", "__Host-shiftbox_session=short", "__Host-shiftbox_session=" + valid + "; __Host-shiftbox_session=" + valid} {
		request := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/session", nil)
		request.Header.Set("Cookie", header)
		if token, err := web.SessionToken(request); err == nil || token != "" {
			t.Error("SessionToken(invalid or ambiguous cookie) returned authority, want rejection")
		}
	}
}

// TestSessionCookieClearMatchesScope removes the original cookie without creating a parent-domain credential.
func TestSessionCookieClearMatchesScope(t *testing.T) {
	response := httptest.NewRecorder()
	web.ClearSessionCookie(response)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("ClearSessionCookie count = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-shiftbox_session" || cookie.Value != "" || cookie.Domain != "" || cookie.Path != "/" || cookie.MaxAge != -1 || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Error("ClearSessionCookie did not delete the exact management cookie scope")
	}
}

// TestSessionCookieRejectsInvalidOutput prevents net/http from silently sanitizing a broken credential.
func TestSessionCookieRejectsInvalidOutput(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat("A", 42) + "B", strings.Repeat("A", 43) + "\r\n"} {
		response := httptest.NewRecorder()
		if err := web.SetSessionCookie(response, token, time.Now().Add(time.Hour)); err == nil || response.Header().Get("Set-Cookie") != "" {
			t.Error("SetSessionCookie(invalid token) emitted a credential, want error and no header")
		}
	}
}
