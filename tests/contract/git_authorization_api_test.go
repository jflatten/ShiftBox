package contract_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGitAuthorizationAPI checks owner-scoped metadata, session and CSRF
// gates, callback host validation, and revocation of outstanding state.
func TestGitAuthorizationAPI(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "git.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, "00000000-0000-4000-8000-000000000003", token, now)
	key, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	gitHub, err := identity.NewGitHub(db, key, "fixture-client", "fixture-secret", 1, clock)
	if err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	handlers := web.GitAuthorizationHandlers(db, origin, clock, gitHub)
	callback := web.GitAuthorizationCallback(origin, gitHub)
	handler := func(id string) *web.AuthorizedHandler {
		route := handlers[id]
		return &route
	}
	request := func(handler http.Handler, method, uri, cookie, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, uri, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: cookie})
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != want || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s = %d, cache %q, want %d and no-store", method, req.URL.Path, recorder.Code, recorder.Header().Get("Cache-Control"), want)
		}
		return recorder
	}
	base := origin + "/api/v1/git/authorization"
	csrf := auth.CSRFToken(token)
	request(handler("beginGitAuthorization"), "POST", base, "", csrf, 401)
	request(handler("beginGitAuthorization"), "POST", base, token, "wrong", 403)
	metadata := request(handler("getGitAuthorization"), "GET", base, token, "", 200)
	if !strings.Contains(metadata.Body.String(), `"state":"revoked"`) || strings.Contains(metadata.Body.String(), "fixture-secret") {
		t.Error("GET Git authorization did not return credential-free metadata for an unlinked owner")
	}
	request(handler("beginGitAuthorization"), "POST", base, token, csrf, 200)
	var owner string
	if err := db.QueryRow("SELECT user_id FROM github_oauth_states").Scan(&owner); err != nil || owner != "member" {
		t.Fatalf("begin authorization stored owner %q, %v, want member", owner, err)
	}
	request(handler("revokeGitAuthorization"), "DELETE", base, token, csrf, 204)
	var remaining int
	if err := db.QueryRow("SELECT count(*) FROM github_oauth_states").Scan(&remaining); err != nil || remaining != 0 {
		t.Errorf("revoke left %d states, %v, want zero", remaining, err)
	}
	request(callback, "GET", "https://foreign.test/api/v1/git/authorization/callback?code=code&state=invalid", "", "", 400)
	request(callback, "GET", base+"/callback?code=code&state=invalid", "", "", 400)

	// An unconfigured App still requires session and CSRF checks and
	// reports a controlled failure once the request is authorized.
	unconfigured := web.GitAuthorizationHandlers(db, origin, clock, nil)["beginGitAuthorization"]
	request(&unconfigured, "POST", base, "", csrf, 401)
	request(&unconfigured, "POST", base, token, "wrong", 403)
	result := request(&unconfigured, "POST", base, token, csrf, 503)
	if !strings.Contains(result.Body.String(), "git.unavailable") {
		t.Error("unconfigured GitHub App did not report its availability problem")
	}
	request(web.GitAuthorizationCallback(origin, nil), "GET", base+"/callback?code=code&state=fixture", "", "", 503)
}
