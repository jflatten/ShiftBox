package app

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/identity"
	"golang.org/x/oauth2"
)

// TestActiveLoginKeepsAttemptsIndependent checks that two tabs can keep their
// sign-in forms open for more than five minutes without sharing a verifier.
func TestActiveLoginKeepsAttemptsIndependent(t *testing.T) {
	login := &SetupLogin{
		Provider: &identity.OIDC{},
		Config: oauth2.Config{
			RedirectURL: "https://manage.test/auth/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://manage.test/dex/auth"},
		},
	}
	handler, err := NewActiveLoginHandler(&sql.DB{}, "installation", login, time.Now, func() string { return "session" })
	if err != nil {
		t.Fatal(err)
	}
	start := func() (*http.Cookie, string) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://manage.test/auth/login", nil))
		response := w.Result()
		defer response.Body.Close()
		cookies := response.Cookies()
		if response.StatusCode != http.StatusSeeOther || len(cookies) != 1 {
			t.Fatalf("login start returned status %d and %d cookies, want 303 and one cookie", response.StatusCode, len(cookies))
		}
		location, err := response.Location()
		if err != nil {
			t.Fatal(err)
		}
		return cookies[0], location.Query().Get("state")
	}
	first, firstState := start()
	second, secondState := start()
	if first.Name == second.Name || firstState == secondState {
		t.Error("two local sign-in attempts share a browser cookie or state")
	}
	if first.MaxAge != int((24*time.Hour)/time.Second) {
		t.Errorf("login cookie lifetime = %d seconds, want the 24-hour Dex authorization window", first.MaxAge)
	}
	if !first.Secure || !first.HttpOnly || first.SameSite != http.SameSiteLaxMode || first.Domain != "" || first.Path != "/" {
		t.Error("login cookie lacks host-only HTTPS protections")
	}

	callback := &url.URL{Scheme: "https", Host: "manage.test", Path: "/auth/callback"}
	callback.RawQuery = url.Values{"code": {"fixture"}, "state": {firstState}}.Encode()
	r := httptest.NewRequest(http.MethodGet, callback.String(), nil)
	r.AddCookie(second)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login#sign-in-retry" {
		t.Errorf("callback with a missing attempt cookie returned %d with location %q, want 303 to a fresh login", w.Code, w.Header().Get("Location"))
	}
	if cookies := w.Header().Values("Set-Cookie"); len(cookies) != 0 {
		t.Errorf("callback with a missing attempt cookie set %d cookies, want zero", len(cookies))
	}
}
