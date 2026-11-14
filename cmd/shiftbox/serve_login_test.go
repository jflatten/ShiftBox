package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/crypto/bcrypt"
)

// TestActiveLoginUsesCurrentMembership drives native Dex and shows that a
// verified password creates a session only for a current active local member.
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
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "active.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const installation = "719b54c3-6e15-499c-a0ed-407e1b316a90"
	const uuidOwner = "bf505ecf-2954-4504-8c88-581a09b37c05"
	const email = "operator@example.test"
	const password = "disposable-fixture-password"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','release','release','now','now',1)`, installation)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,must_change_password,created_by,created_at,updated_at,version)
		VALUES($1,$2,'administrator','active','local',0,'setup','now','now',1)`, uuidOwner, email)
	var stopDex func()
	server, err := app.ListenBootstrap("127.0.0.1:0", cert, func(origin string) (http.Handler, error) {
		admin, login, _, cleanup, err := startSetupDex(ctx, dir, binary, origin, cert, nil)
		if err != nil {
			return nil, err
		}
		stopDex = cleanup
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		if err := admin.Ensure(ctx, email, uuidOwner, hash); err != nil {
			return nil, err
		}
		signIn, err := app.NewActiveLoginHandler(db, installation, login, time.Now, uuid.NewString)
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle("/auth/", signIn)
		mux.Handle("GET /{$}", web.NewSessionHandler(db, origin, &proxy.Registry{}, time.Now))
		return mux, nil
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
	signIn := func(wantStatus int, wantMode string) {
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
		response, err = submitDexForm(client, response, url.Values{"login": {email}, "password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(response.Request.URL.Path, "/approval") {
			response, err = submitDexForm(client, response, url.Values{"approval": {"approve"}})
			if err != nil {
				t.Fatal(err)
			}
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != wantStatus || (wantMode != "" && !bytes.Contains(body, []byte(`"mode":"`+wantMode+`"`))) {
			t.Fatalf("login status = %d and expected mode %q present = %t, want status %d, read error %v",
				response.StatusCode, wantMode, bytes.Contains(body, []byte(wantMode)), wantStatus, err)
		}
	}
	signIn(200, "normal")
	exec("UPDATE users SET must_change_password=1 WHERE id=$1", uuidOwner)
	signIn(200, "password_change")
	exec("UPDATE users SET membership_state='disabled' WHERE id=$1", uuidOwner)
	signIn(401, "")
	exec("UPDATE users SET membership_state='active',credential_mode='external' WHERE id=$1", uuidOwner)
	signIn(401, "")
	exec("UPDATE users SET credential_mode='local' WHERE id=$1", uuidOwner)
	exec("UPDATE installations SET state='configuring'")
	signIn(401, "")
	var total int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&total); err != nil || total != 2 {
		t.Fatalf("session count = %d, %v, want only two sessions", total, err)
	}
}
