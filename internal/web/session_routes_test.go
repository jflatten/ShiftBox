package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/web"
)

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
