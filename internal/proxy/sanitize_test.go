package proxy_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestSanitizeKeepsManagementAuthorityOutOfGuests preserves app cookies while stripping controller authority.
func TestSanitizeKeepsManagementAuthorityOutOfGuests(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://preview.example.test/", nil)
	request.Header.Add("Cookie", "__Host-shiftbox_session=management; app_session=application")
	request.Header.Add("Cookie", "__Host-shiftbox_origin=grant; shiftbox_csrf=csrf")
	request.Header.Set("X-CSRF-Token", "management-token")
	request.Header.Set("X-ShiftBox-Origin-Grant", "entry-grant")
	request.Header.Set("X-ShiftBox-User", "forged-owner")
	request.Header.Set("Connection", "keep-alive, X-Private-Hop")
	request.Header.Set("X-Private-Hop", "connection-local")
	request.Header.Set("Proxy-Authorization", "proxy-credential")
	request.Header.Set("Keep-Alive", "timeout=5")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("X-Application", "preserved")
	proxy.Sanitize(request)
	for _, name := range []string{"X-CSRF-Token", "X-ShiftBox-Origin-Grant", "X-ShiftBox-User", "Connection", "X-Private-Hop", "Proxy-Authorization", "Keep-Alive", "Upgrade"} {
		if got := request.Header.Get(name); got != "" {
			t.Errorf("Sanitize retained %s = %q", name, got)
		}
	}
	if cookies := request.Cookies(); len(cookies) != 1 || cookies[0].Name != "app_session" || cookies[0].Value != "application" {
		t.Errorf("Sanitize cookies = %v, want only application session", cookies)
	}
	if request.Header.Get("X-Application") != "preserved" {
		t.Error("Sanitize removed application header")
	}
}

// TestSanitizeConnectionTokens handles repeated connection fields and noncanonical header keys.
func TestSanitizeConnectionTokens(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://preview.example.test/", nil)
	request.Header["connection"] = []string{"x-first", "x-second"}
	request.Header["x-first"] = []string{"one"}
	request.Header["X-Second"] = []string{"two"}
	request.Header["x-shiftbox-session"] = []string{"secret"}
	proxy.Sanitize(request)
	for key := range request.Header {
		if strings.EqualFold(key, "connection") || strings.EqualFold(key, "x-first") || strings.EqualFold(key, "x-second") || strings.HasPrefix(strings.ToLower(key), "x-shiftbox-") {
			t.Errorf("Sanitize retained noncanonical header %q", key)
		}
	}
}

// TestRegistryClosesOnlyRevokedScope protects unrelated users during offboarding.
func TestRegistryClosesOnlyRevokedScope(t *testing.T) {
	var registry proxy.Registry
	closed := make(map[string]int)
	for _, user := range []string{"alice", "bob"} {
		_, err := registry.Register(proxy.Scope{UserID: user}, func() error { return nil }, func() error { closed[user]++; return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.UserID == "alice" }); err != nil {
		t.Fatal(err)
	}
	if closed["alice"] != 1 || closed["bob"] != 0 {
		t.Errorf("closed = %v, want only alice once", closed)
	}
	if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.UserID == "alice" }); err != nil {
		t.Fatal(err)
	}
	if closed["alice"] != 1 {
		t.Error("closed connection remained registered")
	}
}

// TestRegistryRechecksAdmission denies a connection whose permission disappeared before registration.
func TestRegistryRechecksAdmission(t *testing.T) {
	var registry proxy.Registry
	denied := errors.New("session revoked")
	if _, err := registry.Register(proxy.Scope{UserID: "alice"}, func() error { return denied }, func() error { return nil }); !errors.Is(err, denied) {
		t.Errorf("Register(revoked) = %v, want denial", err)
	}
}

// TestRegistryRetainsFailedClose makes an incomplete revocation visible and retryable.
func TestRegistryRetainsFailedClose(t *testing.T) {
	var registry proxy.Registry
	attempts := 0
	_, err := registry.Register(proxy.Scope{UserID: "alice"}, func() error { return nil }, func() error {
		attempts++
		if attempts == 1 {
			return errors.New("injected close failure")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	match := func(proxy.Scope) bool { return true }
	if err := registry.CloseMatching(match); err == nil {
		t.Error("CloseMatching hid failed connection closure")
	}
	if err := registry.CloseMatching(match); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("close attempts = %d, want 2", attempts)
	}
}
