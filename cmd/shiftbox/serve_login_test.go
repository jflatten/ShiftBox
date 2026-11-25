package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/crypto/bcrypt"
)

// TestControllerDexRequiresRetainedSQLite checks that active startup never
// creates a new identity store when the installation's Dex database is missing.
func TestControllerDexRequiresRetainedSQLite(t *testing.T) {
	dir := t.TempDir()
	_, _, _, _, cleanup, err := startControllerDex(t.Context(), dir, "", "https://management.test", nil)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("controller Dex accepted an absent SQLite identity database")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("identity startup left %d files, error %v, want an empty directory", len(entries), err)
	}
}

// TestActiveLoginUsesCurrentMembership drives native Dex and shows that a
// verified password opens Projects for an active local member, a fresh
// installation issues eight-hour sessions, and a reset forces a password change.
func TestActiveLoginUsesCurrentMembership(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	ctx := t.Context()
	dir := t.TempDir()
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "test-tls.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const installation = "719b54c3-6e15-499c-a0ed-407e1b316a90"
	db, _, err := database.OpenBootstrap(ctx, filepath.Join(dir, "active.db"), database.BootstrapSeed{
		ID: installation, Organization: "ShiftBox", Origin: "https://management.test",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "release", MinGuestRelease: "release", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const uuidOwner = "bf505ecf-2954-4504-8c88-581a09b37c05"
	const email = "operator@example.test"
	const password = "disposable-fixture-password"
	const memberID = "6ea97335-e2cf-4781-9fc0-669b670015df"
	const memberEmail = "member@example.test"
	var postgres *identity.DexPostgres
	if fixture := os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI"); fixture != "" {
		u, err := url.Parse(fixture)
		if err != nil || u.User == nil {
			t.Fatal("invalid PostgreSQL identity fixture")
		}
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil {
			t.Fatal("invalid PostgreSQL identity fixture port")
		}
		secret, _ := u.User.Password()
		postgres = &identity.DexPostgres{Host: u.Hostname(), Port: uint16(port), Database: strings.TrimPrefix(u.Path, "/"),
			User: u.User.Username(), Password: secret, CAFile: os.Getenv("SHIFTBOX_TEST_SETUP_CA")}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE installations SET state='active' WHERE id=$1", installation)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,must_change_password,created_by,created_at,updated_at,version)
		VALUES($1,$2,'administrator','active','local',0,'setup','now','now',1)`, uuidOwner, email)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,must_change_password,created_by,created_at,updated_at,version)
		VALUES($1,$2,'member','active','local',0,'setup','now','now',1)`, memberID, memberEmail)
	bootstrapAdmin, _, _, closeBootstrap, err := startSetupDex(ctx, dir, binary, "https://127.0.0.1:9999", cert, postgres)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err == nil {
		err = bootstrapAdmin.Ensure(ctx, email, uuidOwner, hash)
	}
	if err == nil {
		err = bootstrapAdmin.Ensure(ctx, memberEmail, memberID, hash)
	}
	closeBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	var stopDex func()
	server, err := app.ListenBootstrap("127.0.0.1:0", cert, func(origin string) (http.Handler, error) {
		admin, login, dexWeb, _, cleanup, err := startControllerDex(ctx, dir, binary, origin, postgres)
		if err != nil {
			return nil, err
		}
		stopDex = cleanup
		signIn, err := app.NewActiveLoginHandler(db, installation, login, time.Now, uuid.NewString)
		if err != nil {
			return nil, err
		}
		settings := config.InstallationSettings{ManagementOrigin: origin,
			IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test"}
		state := &app.ControllerState{DB: db, Installation: app.ActiveInstallation{Settings: settings}}
		services, err := app.NewManagementServices(state, operation.NewJournal(db, time.Now, uuid.NewString),
			&proxy.Registry{}, web.WorkspaceLaunchPolicy{}, nil, admin, time.Now)
		if err != nil {
			return nil, err
		}
		api := http.NewServeMux()
		api.Handle("GET /api/v1/session", services.Session)
		readTimeout := services.Handlers["getSessionTimeout"]
		api.Handle("GET /api/v1/admin/settings/session", &readTimeout)
		updateTimeout := services.Handlers["updateSessionTimeout"]
		api.Handle("PUT /api/v1/admin/settings/session", &updateTimeout)
		list := services.Handlers["listWorkspaces"]
		api.Handle("GET /api/v1/workspaces", &list)
		change := services.Handlers["changeOwnPassword"]
		api.Handle("PUT /api/v1/session/password", &change)
		reset := services.Handlers["resetLocalUserPassword"]
		api.Handle("POST /api/v1/admin/users/{userId}/password-reset", &reset)
		assets, err := web.NewStaticHandler(fstest.MapFS{
			"index.html":           {Data: []byte("<h1>ShiftBox</h1>")},
			"templates/index.html": {Data: []byte("<h1>Projects</h1>")},
		})
		if err != nil {
			return nil, err
		}
		return app.NewManagementSite(api, signIn, dexWeb, assets)
	})
	if stopDex != nil {
		defer stopDex()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	defer func() {
		server.Close()
		<-served
	}()
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	probe, err := http.NewRequestWithContext(ctx, http.MethodGet, server.Origin+"/auth/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe.Host = "workspace.ide.test"
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Do(probe)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest || len(response.Cookies()) != 0 {
		t.Fatalf("login on wrong host returned %d with %d cookies, want 421 with no cookies",
			response.StatusCode, len(response.Cookies()))
	}
	for _, path := range []string{"/dex/token", "/dex/keys", "/dex/healthz"} {
		response, err := client.Get(server.Origin + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("browser request to %s returned %d, want 404", path, response.StatusCode)
		}
	}
	signIn := func(email, credential string, wantStatus int, wantMode string) *http.Client {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Jar: jar, Transport: transport, Timeout: 10 * time.Second}
		unbound, err := client.Get(server.Origin + "/auth/callback?code=unexpected&state=unbound")
		if err != nil {
			t.Fatal(err)
		}
		unbound.Body.Close()
		if unbound.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unbound callback returned %d, want 401", unbound.StatusCode)
		}
		response, err := client.Get(server.Origin + "/auth/login")
		if err != nil {
			t.Fatal(err)
		}
		if response.Request.URL.Host != probe.URL.Host || !strings.HasPrefix(response.Request.URL.Path, "/dex/") {
			t.Fatalf("Dex form served from %s%s, want management origin /dex/", response.Request.URL.Host, response.Request.URL.Path)
		}
		if frontend, err := loginFrontendAssets(); err != nil {
			t.Fatal(err)
		} else if frontend != nil {
			// An incorrect password should stay on the branded form with a generic
			// error and an empty password field before the user retries.
			res, err := submitDexForm(client, response, url.Values{"login": {email}, "password": {"incorrect-fixture-password"}})
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || !bytes.Contains(body, []byte("Log in to ShiftBox")) ||
				!bytes.Contains(body, []byte("Incorrect email or password. Try again.")) ||
				bytes.Contains(body, []byte("incorrect-fixture-password")) {
				t.Fatal("failed login did not render the branded retry form safely")
			}
			response, err = client.Get(server.Origin + "/auth/login")
			if err != nil {
				t.Fatal(err)
			}
		}
		response, err = submitDexForm(client, response, url.Values{"login": {email}, "password": {credential}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(response.Request.URL.Path, "/approval") {
			response.Body.Close()
			t.Fatal("first-party sign-in displayed a separate consent screen")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if wantMode != "" && err == nil {
			wantPath, wantPage := "/", "ShiftBox"
			// An unrestricted login opens Projects. A temporary password still
			// leads to the password-change entry point before work is available.
			if wantMode == "normal" {
				wantPath, wantPage = "/templates/", "Projects"
			}
			if response.StatusCode != http.StatusOK || response.Request.URL.Path != wantPath || !bytes.Contains(body, []byte(wantPage)) {
				t.Fatalf("active sign-in (%s) returned %d at %s, want 200 at %s with %s", wantMode, response.StatusCode, response.Request.URL.Path, wantPath, wantPage)
			}
			response, err = client.Get(server.Origin + "/api/v1/session")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err = io.ReadAll(response.Body)
		}
		if err != nil || response.StatusCode != wantStatus || (wantMode != "" && !bytes.Contains(body, []byte(`"mode":"`+wantMode+`"`))) {
			t.Fatalf("login status = %d and expected mode %q present = %t, want status %d, read error %v",
				response.StatusCode, wantMode, bytes.Contains(body, []byte(wantMode)), wantStatus, err)
		}
		if wantMode == "normal" {
			workspaces, err := client.Get(server.Origin + "/api/v1/workspaces")
			if err != nil {
				t.Fatal(err)
			}
			defer workspaces.Body.Close()
			if workspaces.StatusCode != http.StatusOK {
				t.Fatalf("signed-in workspace list status = %d, want 200", workspaces.StatusCode)
			}
		}
		return client
	}
	expired := signIn(email, password, 200, "normal")
	// Saving the timeout takes effect on the next Dex sign-in. Read it back
	// through the administrator API before expiring the original cookie.
	response, err = expired.Get(server.Origin + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	var originalSession struct {
		CSRFToken string    `json:"csrfToken"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	err = json.NewDecoder(response.Body).Decode(&originalSession)
	response.Body.Close()
	if err != nil || originalSession.CSRFToken == "" {
		t.Fatalf("reading original session failed: %v", err)
	}
	if remaining := time.Until(originalSession.ExpiresAt); remaining < 8*time.Hour-time.Minute || remaining > 8*time.Hour {
		t.Errorf("fresh regular session has %v remaining, want approximately 8 hours", remaining)
	}
	const timeoutPath = "/api/v1/admin/settings/session"
	update, err := http.NewRequestWithContext(ctx, http.MethodPut, server.Origin+timeoutPath, strings.NewReader(`{"value":2,"unit":"hours"}`))
	if err != nil {
		t.Fatal(err)
	}
	update.Header.Set("Origin", server.Origin)
	update.Header.Set("X-CSRF-Token", originalSession.CSRFToken)
	update.Header.Set("If-Match", `"1"`)
	response, err = expired.Do(update)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("session timeout update returned %d, want 204", response.StatusCode)
	}
	response, err = expired.Get(server.Origin + timeoutPath)
	if err != nil {
		t.Fatal(err)
	}
	var savedTimeout auth.SessionTimeout
	err = json.NewDecoder(response.Body).Decode(&savedTimeout)
	response.Body.Close()
	if err != nil || savedTimeout != (auth.SessionTimeout{Value: 2, Unit: "hours", Version: 2}) {
		t.Fatalf("reloaded session timeout returned %+v, %v; want two hours at version 2", savedTimeout, err)
	}
	// Expiry opens a sign-in page in each tab. They share a cookie jar, but
	// signing in from one tab must work even after the other opened its form.
	exec("UPDATE sessions SET expires_at=$1 WHERE user_id=$2", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), uuidOwner)
	response, err = expired.Get(server.Origin + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session returned %d, want 401", response.StatusCode)
	}
	firstTab, err := expired.Get(server.Origin + "/auth/login#session-expired")
	if err != nil {
		t.Fatal(err)
	}
	defer firstTab.Body.Close()
	otherTab, err := expired.Get(server.Origin + "/auth/login#session-expired")
	if err != nil {
		t.Fatal(err)
	}
	defer otherTab.Body.Close()
	for _, form := range []*http.Response{firstTab, otherTab} {
		res, err := submitDexForm(expired, form, url.Values{"login": {email}, "password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Request.URL.Path != "/templates/" {
			t.Fatalf("sign-in after session expiry returned %d at %s, want 200 at /templates/", res.StatusCode, res.Request.URL.Path)
		}
	}
	response, err = expired.Get(server.Origin + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	var renewedSession struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	err = json.NewDecoder(response.Body).Decode(&renewedSession)
	response.Body.Close()
	if remaining := time.Until(renewedSession.ExpiresAt); err != nil || remaining < 119*time.Minute || remaining > 120*time.Minute {
		t.Fatalf("renewed session has %s remaining, %v; want a two-hour session", remaining, err)
	}
	exec("UPDATE users SET must_change_password=1 WHERE id=$1", uuidOwner)
	restricted := signIn(email, password, 200, "password_change")
	const replacement = "disposable-replacement-fixture"
	managementURL, err := url.Parse(server.Origin)
	if err != nil {
		t.Fatal(err)
	}
	sessionToken := func(client *http.Client) string {
		t.Helper()
		for _, cookie := range client.Jar.Cookies(managementURL) {
			if cookie.Name == "__Host-shiftbox_session" {
				return cookie.Value
			}
		}
		t.Fatal("missing management session cookie")
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, server.Origin+"/api/v1/session/password",
		strings.NewReader(`{"currentPassword":"`+password+`","newPassword":"`+replacement+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", server.Origin)
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(sessionToken(restricted)))
	res, err := restricted.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("active password change returned %d, want 204", res.StatusCode)
	}
	operator := signIn(email, replacement, 200, "normal")
	member := signIn(memberEmail, password, 200, "normal")
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, server.Origin+"/api/v1/admin/users/"+memberID+"/password-reset", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", server.Origin)
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(sessionToken(operator)))
	req.Header.Set("If-Match", `"1"`)
	res, err = operator.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var reset struct {
		TemporaryPassword string `json:"temporaryPassword"`
	}
	err = json.NewDecoder(res.Body).Decode(&reset)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || err != nil || reset.TemporaryPassword == "" {
		t.Fatalf("password reset status = %d, want 200 with temporary password (decode error %v)", res.StatusCode, err)
	}
	res, err = member.Get(server.Origin + "/api/v1/session")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want 401", res.StatusCode)
	}
	signIn(memberEmail, password, 401, "")
	signIn(memberEmail, reset.TemporaryPassword, 200, "password_change")
	exec("UPDATE users SET membership_state='disabled' WHERE id=$1", uuidOwner)
	signIn(email, replacement, 401, "")
	exec("UPDATE users SET membership_state='active',credential_mode='external' WHERE id=$1", uuidOwner)
	signIn(email, replacement, 401, "")
	exec("UPDATE users SET credential_mode='local' WHERE id=$1", uuidOwner)
	exec("UPDATE installations SET state='configuring'")
	signIn(email, replacement, 401, "")
	var total int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&total); err != nil || total != 7 {
		t.Fatalf("session count = %d, %v, want seven successful sign-ins", total, err)
	}
	if postgres != nil {
		if _, err := os.Lstat(filepath.Join(dir, "dex.db")); !os.IsNotExist(err) {
			t.Fatal("PostgreSQL identity startup did not leave the local fallback absent")
		}
	}
}
