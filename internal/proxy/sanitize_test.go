package proxy_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestServiceAllowlistRejectsUnregisteredDestinations prevents cross-workspace, stale-generation, and arbitrary-port routing.
func TestServiceAllowlistRejectsUnregisteredDestinations(t *testing.T) {
	allowlist, err := proxy.NewServiceAllowlist("workspace-one", 4, []proxy.Service{{Key: "ide", Protocols: []string{"http", "websocket"}}, {Key: "preview-8080", Protocols: []string{"http"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		workspace     string
		generation    int64
		key, protocol string
		want          bool
	}{
		{"workspace-one", 4, "ide", "http", true},
		{"workspace-one", 4, "ide", "websocket", true},
		{"workspace-one", 4, "preview-8080", "http", true},
		{"workspace-two", 4, "ide", "http", false},
		{"workspace-one", 3, "ide", "http", false},
		{"workspace-one", 4, "preview-8080", "websocket", false},
		{"workspace-one", 4, "preview-9000", "http", false},
		{"workspace-one", 4, "127.0.0.1:22", "http", false},
		{"workspace-one", 4, "http://169.254.169.254/", "http", false},
		{"workspace-one", 4, "ide", "tcp", false},
	} {
		if got := allowlist.Allows(tc.workspace, tc.generation, tc.key, tc.protocol); got != tc.want {
			t.Errorf("Allows(%q,%d,%q,%q) = %t, want %t", tc.workspace, tc.generation, tc.key, tc.protocol, got, tc.want)
		}
	}
}

// TestServiceAllowlistCopiesRegistration prevents later guest-message mutation from expanding admitted protocols.
func TestServiceAllowlistCopiesRegistration(t *testing.T) {
	services := []proxy.Service{{Key: "ide", Protocols: []string{"http"}}}
	allowlist, err := proxy.NewServiceAllowlist("workspace", 1, services)
	if err != nil {
		t.Fatal(err)
	}
	services[0].Protocols[0] = "websocket"
	services[0].Key = "preview-8080"
	if !allowlist.Allows("workspace", 1, "ide", "http") || allowlist.Allows("workspace", 1, "ide", "websocket") || allowlist.Allows("workspace", 1, "preview-8080", "http") {
		t.Error("registration mutation changed the immutable service allowlist")
	}
}

// TestServiceAllowlistRejectsAmbiguousRegistration rejects duplicate keys and protocols outside the HTTP/WebSocket contract.
func TestServiceAllowlistRejectsAmbiguousRegistration(t *testing.T) {
	for _, services := range [][]proxy.Service{
		{{Key: "ide", Protocols: []string{"tcp"}}},
		{{Key: "", Protocols: []string{"http"}}},
		{{Key: "ide"}},
		{{Key: "ide", Protocols: []string{"http"}}, {Key: "ide", Protocols: []string{"websocket"}}},
	} {
		if _, err := proxy.NewServiceAllowlist("workspace", 1, services); err == nil {
			t.Errorf("NewServiceAllowlist(%+v) accepted invalid registration", services)
		}
	}
	for _, workspace := range []string{"", "workspace"} {
		if _, err := proxy.NewServiceAllowlist(workspace, 0, nil); err == nil {
			t.Errorf("NewServiceAllowlist(%q,0) accepted missing authority", workspace)
		}
	}
}

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
