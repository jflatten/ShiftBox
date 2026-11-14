package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestDexExternalRoundTrip counts actual authorization-code exchanges at a
// disposable upstream during enrollment, approved sign-in, and reauthorization.
func TestDexExternalRoundTrip(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	ctx := t.Context()
	dir := t.TempDir()
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "browser.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	admin, _, _, closeSetup, err := startSetupDex(ctx, dir, binary, "https://127.0.0.1:9999", cert, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeSetup()
	if admin.Client == nil {
		t.Fatal("setup did not initialize the retained Dex database")
	}
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(dir, "state.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: "https://management.test", IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active'"); err != nil {
		t.Fatal(err)
	}
	upstream, exchanges := externalOIDCFixture(t)
	caFile := filepath.Join(dir, "upstream.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	var closeDex func()
	server, err := app.ListenBootstrap("127.0.0.1:0", cert, func(origin string) (http.Handler, error) {
		admin, login, dexWeb, _, cleanup, err := startControllerDex(ctx, dir, binary, origin, nil)
		if err != nil {
			return nil, err
		}
		closeDex = cleanup
		const dexID = "shiftbox-external-fixture"
		dex := identity.ExternalConnectorAdmin{Client: admin.Client.(identity.ExternalConnectorAPI)}
		cfg := identity.ExternalConnectorConfig{ID: "company", Issuer: upstream.URL, ClientID: "upstream-client",
			ClientSecret: "upstream-fixture-secret", RootCAFile: caFile}
		if err := dex.Create(ctx, dexID, origin, cfg); err != nil {
			return nil, err
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO external_connector(installation_id,issuer,connector_id,dex_id) VALUES($1,$2,$3,$4)",
			installation, upstream.URL, cfg.ID, dexID); err != nil {
			return nil, err
		}
		signIn, renew, err := app.NewActiveIdentityHandlers(db, installation, login, registry,
			func(ctx context.Context) (identity.ConnectorIdentity, error) {
				return app.CurrentExternalConnector(ctx, db)
			}, time.Now, uuid.NewString)
		if err != nil {
			return nil, err
		}
		api := http.NewServeMux()
		api.Handle("GET /api/v1/session", web.NewSessionHandler(db, origin, registry, time.Now))
		api.Handle("POST /api/v1/session/external-reauthorization", &renew)
		return app.NewManagementSite(api, signIn, dexWeb, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})
	if closeDex != nil {
		defer closeDex()
	}
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() { server.Close(); <-done }()
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	roots.AddCert(upstream.Certificate())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second}
	visit := func(location string, want int) {
		t.Helper()
		res, err := client.Get(location)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if strings.HasSuffix(res.Request.URL.Path, "/approval") {
			res, err = submitDexForm(client, res, url.Values{"approval": {"approve"}})
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
		}
		if res.StatusCode != want {
			t.Fatalf("external visit returned %d at %s, want %d", res.StatusCode, res.Request.URL.Path, want)
		}
	}
	visit(server.Origin+"/auth/external", http.StatusForbidden)
	if exchanges.Load() != 1 {
		t.Fatal("pending membership did not complete an upstream code exchange")
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET membership_state='active' WHERE credential_mode='external' AND membership_state='pending'"); err != nil {
		t.Fatal(err)
	}
	visit(server.Origin+"/auth/external", http.StatusOK)
	if exchanges.Load() != 2 {
		t.Fatal("approved sign-in did not reach upstream")
	}
	originURL, err := url.Parse(server.Origin)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for _, cookie := range jar.Cookies(originURL) {
		if cookie.Name == "__Host-shiftbox_session" {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("approved external sign-in did not issue a session")
	}
	// Dex signs auth_time in seconds. Advance one tick before requesting renewal.
	time.Sleep(time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.Origin+"/api/v1/session/external-reauthorization", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", server.Origin)
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var renewal struct{ AuthorizationURL string }
	err = json.NewDecoder(res.Body).Decode(&renewal)
	res.Body.Close()
	if res.StatusCode != 200 || err != nil || renewal.AuthorizationURL == "" {
		t.Fatalf("begin reauthorization status = %d, decode error %v", res.StatusCode, err)
	}
	visit(renewal.AuthorizationURL, http.StatusOK)
	if exchanges.Load() != 3 {
		t.Fatal("renewal reused broker authority instead of exchanging a new upstream code")
	}
	var revoked bool
	if err := db.QueryRowContext(ctx, "SELECT revoked_at IS NOT NULL FROM sessions WHERE token_digest=$1", auth.TokenDigest(token)).Scan(&revoked); err != nil || !revoked {
		t.Fatal("old session remained active after upstream renewal", err)
	}
}

func externalOIDCFixture(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var exchanges atomic.Int32
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	issuer := "https://" + server.Listener.Addr().String()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeExternalFixture(w, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeExternalFixture(w, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "fixture", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		callback, err := url.Parse(query.Get("redirect_uri"))
		if err != nil || callback.Scheme != "https" || callback.Path != "/dex/callback" {
			http.Error(w, "wrong callback", 400)
			return
		}
		callback.RawQuery = url.Values{"code": {"fixture-code"}, "state": {query.Get("state")}}.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("code") != "fixture-code" || r.FormValue("grant_type") != "authorization_code" {
			http.Error(w, "expected authorization code", 400)
			return
		}
		exchanges.Add(1)
		now := time.Now()
		body, _ := json.Marshal(map[string]any{"iss": issuer, "sub": "upstream-user", "aud": "upstream-client",
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "email": "upstream@example.test", "email_verified": true, "name": "External Fixture"})
		unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"fixture"}`)) + "." + base64.RawURLEncoding.EncodeToString(body)
		digest := sha256.Sum256([]byte(unsigned))
		signed, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "could not sign fixture", 500)
			return
		}
		writeExternalFixture(w, map[string]any{"token_type": "Bearer", "access_token": "fixture", "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(signed)})
	})
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, &exchanges
}

func writeExternalFixture(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		fmt.Fprintln(w, "fixture response failed")
	}
}
