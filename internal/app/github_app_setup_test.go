package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
)

const githubSetupTestOrigin = "https://shiftbox.test:8443"

type githubSetupFixtureTransport func(*http.Request) (*http.Response, error)

func (f githubSetupFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestGitHubAppSetupKeepsUserConsentSeparate exercises the admin-only
// registration and installation callbacks, encrypted persistence and the user's own OAuth entry point.
func TestGitHubAppSetupKeepsUserConsentSeparate(t *testing.T) {
	state, admin, member, secondAdmin, now := newGitHubSetupFixture(t)
	runtime, err := state.ConfigureGitHubAppSetup(t.Context(), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	fixtureKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(fixtureKey)}))
	const clientSecret = "fixture-client-secret-unique"
	var conversions, installations int
	state.githubAppSetup.client.Transport = githubSetupFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatalf("GitHub API request reached %s", r.URL.Host)
		}
		var body any
		switch {
		case r.Method == "POST" && r.URL.Path == "/app-manifests/abc123/conversions":
			conversions++
			body = map[string]any{"id": 1234, "slug": "shiftbox-fixture", "client_id": "Iv1.fixture-client",
				"client_secret": clientSecret, "pem": privateKey, "webhook_secret": "unused-webhook-fixture",
				"external_url": githubSetupTestOrigin,
				"owner":        map[string]any{"id": 99, "login": "sample-org", "type": "Organization"},
				"permissions": map[string]string{"contents": "write", "pull_requests": "write", "workflows": "write",
					"metadata": "read", "checks": "read", "statuses": "read"}}
		case r.Method == "GET" && r.URL.Path == "/app/installations":
			installations++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Fatal("installation verification did not use an app JWT")
			}
			owner, appID := 12, 444
			if installations > 1 {
				owner, appID = 99, 1234
			}
			body = []any{map[string]any{"id": 4321, "app_id": appID, "repository_selection": "selected",
				"account": map[string]any{"id": owner, "login": "sample-org", "type": "Organization"},
				"permissions": map[string]string{"contents": "write", "pull_requests": "write", "workflows": "write",
					"metadata": "read", "checks": "read", "statuses": "read"}}}
		default:
			t.Fatalf("unexpected GitHub API request %s %s", r.Method, r.URL.Path)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
	})
	handlers := state.githubAppHandlers(func() time.Time { return now })
	request := func(id, token, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		handler := handlers[id]
		r := httptest.NewRequest(method, githubSetupTestOrigin+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", githubSetupTestOrigin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s as requested role returned %d %s, want %d", id, w.Code, w.Body, want)
		}
		if strings.Contains(w.Body.String(), clientSecret) || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatal("setup response included application credentials")
		}
		return w
	}
	const settings = "/api/v1/admin/settings/github-app"
	body := `{"organization":"sample-org"}`
	request("beginGitHubAppSetup", member, "POST", settings, body, 403)
	created := request("beginGitHubAppSetup", admin, "POST", settings, body, 200)
	var post struct {
		Action   string
		Manifest string
	}
	if err := json.Unmarshal(created.Body.Bytes(), &post); err != nil {
		t.Fatal(err)
	}
	action, err := url.Parse(post.Action)
	if err != nil || action.Scheme != "https" || action.Host != "github.com" ||
		action.Path != "/organizations/sample-org/settings/apps/new" || action.Query().Get("state") == "" {
		t.Fatalf("manifest action = %q (%v), want organization-scoped GitHub form with state", post.Action, err)
	}
	var manifest struct {
		Public       bool
		RedirectURL  string                `json:"redirect_url"`
		SetupURL     string                `json:"setup_url"`
		CallbackURLs []string              `json:"callback_urls"`
		RequestOAuth bool                  `json:"request_oauth_on_install"`
		Permissions  map[string]string     `json:"default_permissions"`
		Hook         struct{ Active bool } `json:"hook_attributes"`
	}
	if err := json.Unmarshal([]byte(post.Manifest), &manifest); err != nil || manifest.Public || manifest.RequestOAuth || manifest.Hook.Active ||
		len(manifest.Permissions) != 6 || manifest.Permissions["workflows"] != "write" ||
		len(manifest.CallbackURLs) != 1 || manifest.CallbackURLs[0] != githubSetupTestOrigin+"/api/v1/git/authorization/callback" ||
		manifest.RedirectURL != githubSetupTestOrigin+settings+"/return" ||
		manifest.SetupURL != githubSetupTestOrigin+"/integrations/?github-install=1" {
		t.Fatalf("manifest = %s (%v), want a private app with separate OAuth and Integrations return URLs", post.Manifest, err)
	}
	callback := settings + "/callback"
	payload := fmt.Sprintf(`{"code":"abc123","state":%q}`, action.Query().Get("state"))
	now = now.Add(31 * time.Minute)
	request("completeGitHubAppRegistration", admin, "POST", callback, payload, 400)
	fresh := request("beginGitHubAppSetup", admin, "POST", settings, body, 200)
	if err := json.Unmarshal(fresh.Body.Bytes(), &post); err != nil {
		t.Fatal(err)
	}
	action, err = url.Parse(post.Action)
	if err != nil {
		t.Fatal(err)
	}
	payload = fmt.Sprintf(`{"code":"abc123","state":%q}`, action.Query().Get("state"))
	request("completeGitHubAppRegistration", secondAdmin, "POST", callback, payload, 400)
	request("completeGitHubAppRegistration", admin, "POST", callback, `{"code":"abc123","state":"wrong"}`, 400)
	result := request("completeGitHubAppRegistration", admin, "POST", callback, payload, 200)
	if !strings.Contains(result.Body.String(), "https://github.com/apps/shiftbox-fixture/installations/new") || runtime.Active() != nil {
		t.Fatal("registration did not continue to repository installation with user access still unavailable")
	}
	request("completeGitHubAppRegistration", admin, "POST", callback, payload, 400)
	request("verifyGitHubAppInstallation", member, "POST", settings+"/verify", "", 403)
	pending := request("verifyGitHubAppInstallation", admin, "POST", settings+"/verify?installation_id=4321", "", 200)
	if !strings.Contains(pending.Body.String(), "installation_pending") || runtime.Active() != nil {
		t.Fatal("a browser-supplied installation ID bypassed verification")
	}
	ready := request("verifyGitHubAppInstallation", admin, "POST", settings+"/verify", "", 200)
	if !strings.Contains(ready.Body.String(), `"state":"ready"`) || runtime.Active() == nil {
		t.Fatalf("verified installation did not enable Account → Connect GitHub: %s", ready.Body)
	}
	if conversions != 1 || installations != 2 {
		t.Fatalf("GitHub calls = %d conversions and %d installation checks, want 1 and 2", conversions, installations)
	}
	var ciphertext string
	if err := state.DB.QueryRow("SELECT value_json FROM protected_values WHERE id=$1", state.githubAppSetup.binding().RecordID).Scan(&ciphertext); err != nil ||
		strings.Contains(ciphertext, clientSecret) || strings.Contains(ciphertext, "PRIVATE KEY") {
		t.Fatal("GitHub App credentials were not stored as protected data")
	}
	runtime, err = state.ConfigureGitHubAppSetup(t.Context(), nil, func() time.Time { return now })
	if err != nil || runtime.Active() == nil {
		t.Fatalf("GitHub setup did not survive a restart: %v", err)
	}
	userRoutes := web.GitAuthorizationHandlers(state.DB, githubSetupTestOrigin, func() time.Time { return now }, runtime)
	requestOAuth := httptest.NewRequest("GET", githubSetupTestOrigin+"/api/v1/git/authorization", nil)
	requestOAuth.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: member})
	view := httptest.NewRecorder()
	handler := userRoutes["getGitAuthorization"]
	handler.ServeHTTP(view, requestOAuth)
	if view.Code != 200 || !strings.Contains(view.Body.String(), `"state":"revoked"`) {
		t.Fatalf("member's authorization after admin installation = %d %s, want independent consent", view.Code, view.Body)
	}
}

// TestGitHubAppRevocation checks stale and unauthorized requests, GitHub's
// asynchronous uninstall, and durable recovery from an ambiguous provider failure.
func TestGitHubAppRevocation(t *testing.T) {
	state, admin, member, _, now := newGitHubSetupFixture(t)
	clock := func() time.Time { return now }
	_, err := state.ConfigureGitHubAppSetup(t.Context(), nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	record := githubAppRecord{
		App: &githubAppRegistration{ID: 1234, Name: "Fixture", Slug: "fixture", ClientID: "Iv1.fixture",
			ClientSecret: "fixture-secret",
			PrivateKey:   string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
			Owner:        githubAppOwner{ID: 99, Login: "sample-org", Type: "Organization"}},
		InstallationID: 4321, RepositorySelection: "selected",
	}
	if err := database.Transaction(t.Context(), state.DB, func(tx *sql.Tx) error {
		_, err := state.githubAppSetup.write(t.Context(), tx, "", record)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := state.ConfigureGitHubAppSetup(t.Context(), nil, clock)
	if err != nil || runtime.Active() == nil {
		t.Fatalf("load installed fixture: %v", err)
	}
	var owner string
	if err := state.DB.QueryRow("SELECT id FROM users WHERE role='member'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := state.DB.Exec(`INSERT INTO github_authorizations(user_id,app_id,provider_user_id,state,nonce,ciphertext,
		access_expires_at,refresh_expires_at,updated_at,version)
		VALUES($1,$2,'42','connected',$3,$3,'','','fixture',1)`, owner, record.App.ClientID, []byte("fixture-encrypted")); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Active().BeginAuthorization(t.Context(), owner, githubSetupTestOrigin); err != nil {
		t.Fatal(err)
	}
	request := func(operation, method, path, body, session string, csrf bool, want int) string {
		t.Helper()
		handler, exists := state.githubAppHandlers(clock)[operation]
		if !exists {
			t.Fatalf("handler %s is missing", operation)
		}
		r := httptest.NewRequest(method, githubSetupTestOrigin+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: session})
		r.Header.Set("Origin", githubSetupTestOrigin)
		r.Header.Set("Content-Type", "application/json")
		if csrf {
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(session))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status = %d %s, want %d", operation, w.Code, w.Body, want)
		}
		return w.Body.String()
	}
	const endpoint = "/api/v1/admin/settings/github-app"
	revoke := func(id, session string, csrf bool, want int) string {
		t.Helper()
		return request("revokeGitHubAppInstallation", "POST", endpoint+"/revoke",
			fmt.Sprintf(`{"installationId":%q}`, id), session, csrf, want)
	}
	deletes := 0
	deleteStatuses := []int{502, 202, 404}
	transport := githubSetupFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Fatalf("GitHub uninstall request to %s must use an app JWT", r.URL.Host)
		}
		status, body := 200, "[]"
		switch {
		case r.Method == "DELETE" && r.URL.Path == "/app/installations/4321":
			if deletes >= len(deleteStatuses) {
				t.Fatal("completed revocation sent another provider request")
			}
			status = deleteStatuses[deletes]
			deletes++
		case r.Method == "GET" && r.URL.Path == "/app/installations":
			if deletes == 2 {
				body = `[{"id":4321}]`
			}
		default:
			t.Fatalf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	state.githubAppSetup.client.Transport = transport
	revoke("4321", member, true, 403)
	revoke("4321", admin, false, 403)
	revoke("9999", admin, true, 409)
	if deletes != 0 || runtime.Active() == nil {
		t.Fatal("denied revocation affected the running installation")
	}
	revoke("4321", admin, true, 502)
	if runtime.Active() != nil {
		t.Fatal("GitHub remained enabled after a confirmed revocation request")
	}
	var authState string
	var ciphertext []byte
	var outstanding int
	if err := state.DB.QueryRow(`SELECT state,ciphertext,(SELECT count(*) FROM github_oauth_states WHERE app_id=$1)
		FROM github_authorizations WHERE user_id=$2 AND app_id=$1`, record.App.ClientID, owner).
		Scan(&authState, &ciphertext, &outstanding); err != nil || authState != "reconnect_required" || len(ciphertext) != 0 || outstanding != 0 {
		t.Fatalf("revoked app authorization state = %q, token bytes = %d, unfinished OAuth states = %d, error = %v",
			authState, len(ciphertext), outstanding, err)
	}
	runtime, err = state.ConfigureGitHubAppSetup(t.Context(), nil, clock)
	if err != nil || runtime.Active() != nil {
		t.Fatalf("restart revived a pending revocation: %v", err)
	}
	state.githubAppSetup.client.Transport = transport
	if body := revoke("4321", admin, true, 200); !strings.Contains(body, `"state":"revoking"`) {
		t.Fatalf("accepted but unfinished uninstall = %s, want revoking", body)
	}
	if body := revoke("4321", admin, true, 200); !strings.Contains(body, `"state":"installation_pending"`) || !strings.Contains(body, "/apps/fixture/installations/new") {
		t.Fatalf("finished uninstall = %s, want the registered app available for reinstall", body)
	}
	revoke("4321", admin, true, 200)
	if deletes != 3 {
		t.Fatalf("provider DELETE count = %d, want 3", deletes)
	}
	var audited int
	if err := state.DB.QueryRow("SELECT count(*) FROM audit_events WHERE action='github_app.revoke'").Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("completed revocation audit count = %d, error = %v, want 1", audited, err)
	}
	reloaded, _, err := state.githubAppSetup.read(t.Context(), state.DB)
	if err != nil || reloaded.App == nil || reloaded.InstallationID != 0 || reloaded.RepositorySelection != "" {
		t.Fatalf("read after revoke has app %t, installation %d, selection %q, error %v", reloaded.App != nil, reloaded.InstallationID, reloaded.RepositorySelection, err)
	}
	state.githubAppSetup.client.Transport = githubSetupFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/app/installations" {
			t.Fatalf("unexpected request during reinstall: %s %s", r.Method, r.URL.Path)
		}
		body, err := json.Marshal([]githubAppInstallation{{
			ID: 4322, AppID: record.App.ID, Account: record.App.Owner,
			RepositorySelection: "selected", Permissions: githubPermissions,
		}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, err
	})
	if body := request("verifyGitHubAppInstallation", "POST", endpoint+"/verify", "", admin, true, 200); !strings.Contains(body, `"state":"ready"`) {
		t.Fatalf("reinstall = %s, want ready", body)
	}
	gitHandlers := web.GitAuthorizationHandlers(state.DB, githubSetupTestOrigin, clock, runtime)
	getAuthorization := gitHandlers["getGitAuthorization"]
	r := httptest.NewRequest(http.MethodGet, githubSetupTestOrigin+"/api/v1/git/authorization", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: member})
	w := httptest.NewRecorder()
	getAuthorization.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"reconnect_required"`) {
		t.Fatalf("member status after reinstall = %d %s, want reconnect_required", w.Code, w.Body)
	}
}

func newGitHubSetupFixture(t *testing.T) (*ControllerState, string, string, string, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	db, id, err := database.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "setup.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: githubSetupTestOrigin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	err = database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE installations SET state='active'"); err != nil {
			return err
		}
		for i, role := range []string{"administrator", "member", "administrator"} {
			user, err := auth.NewStore(db).CreateUser(t.Context(), tx, auth.User{ID: uuid.NewString(),
				PrimaryEmail: fmt.Sprintf("user%d@example.test", i), Role: role, MembershipState: "active", CredentialMode: "local"}, "fixture", now)
			if err != nil {
				return err
			}
			session, token, err := auth.NewSession(uuid.NewString(), user.ID, "normal", now, time.Hour)
			if err != nil {
				return err
			}
			if err := auth.NewStore(db).SaveSession(t.Context(), tx, session); err != nil {
				return err
			}
			tokens = append(tokens, token)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ControllerState{DB: db, cipher: key, Installation: ActiveInstallation{InstallationID: id,
			Settings: config.InstallationSettings{ManagementOrigin: githubSetupTestOrigin, OrganizationName: "ShiftBox"}}},
		tokens[0], tokens[1], tokens[2], now
}
