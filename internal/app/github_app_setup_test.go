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
		!strings.HasPrefix(manifest.SetupURL, githubSetupTestOrigin+"/settings/") {
		t.Fatalf("manifest = %s (%v), want prefilled private application and separate OAuth callback", post.Manifest, err)
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
