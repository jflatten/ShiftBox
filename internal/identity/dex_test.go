package identity_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/dexidp/dex/api/v2"
	"github.com/jflatten/ShiftBox/internal/identity"
	"golang.org/x/crypto/bcrypt"
)

func oidcFixture(t *testing.T) (*identity.OIDC, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			value = map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}
		case "/keys":
			value = map[string]any{"keys": []any{map[string]string{"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			t.Errorf("write OIDC fixture: %v", err)
		}
	}))
	issuer = server.URL
	t.Cleanup(server.Close)
	provider, err := identity.NewOIDC(context.Background(), issuer, "shiftbox", server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return provider, key, issuer
}

// TestOIDCRejectsPlaintextKeyDiscovery prevents an HTTPS issuer redirecting signature trust to plaintext HTTP.
func TestOIDCRejectsPlaintextKeyDiscovery(t *testing.T) {
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": "http://127.0.0.1:5559/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	issuer = server.URL
	if _, err := identity.NewOIDC(context.Background(), issuer, "shiftbox", server.Client(), time.Now); !errors.Is(err, identity.ErrInvalidIdentity) {
		t.Errorf("NewOIDC(plaintext JWKS) = %v, want invalid identity", err)
	}
}

// TestDexCancellationDuringProbe distinguishes normal controller shutdown from identity health failure.
func TestDexCancellationDuringProbe(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dex-fixture")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Dex Version: v2.46.1\\n'; exit 0; fi\nexec /bin/sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	probing := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(200)
			return
		}
		close(probing)
		<-r.Context().Done()
	}))
	defer server.Close()
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: filepath.Join(t.TempDir(), "dex.json"), HealthURL: server.URL, Client: server.Client(), PollInterval: 5 * time.Millisecond, StartupTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { defer close(finished); finished <- process.Run(ctx) }() // Cleanup always cancels and joins the child.
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("probe cancellation did not stop child")
		}
	})
	select {
	case <-probing:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("second health probe did not begin")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run(cancel during probe) = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe remained blocked after cancellation")
	}
}

// TestPinnedDexProcess validates generated configuration and HTTPS discovery against the real release binary.
func TestPinnedDexProcess(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned native Dex binary")
	}
	// Keep reservations open until all three distinct ports have been selected;
	// closing each immediately can let the OS return the same ephemeral port.
	var reservations []net.Listener
	t.Cleanup(func() {
		for _, listener := range reservations {
			listener.Close()
		}
	})
	reserveAddress := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, listener)
		return listener.Addr().String()
	}
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	pair := fixture.TLS.Certificates[0]
	client := fixture.Client()
	client.Timeout = 2 * time.Second
	fixture.Close()
	cfg := validDexConfig(t)
	cfg.WebAddress = reserveAddress()
	cfg.TelemetryAddress = reserveAddress()
	cfg.GRPCAddress = reserveAddress()
	adminCertificate, adminPEM := adminCertificateFixture(t)
	cfg.Issuer = "https://" + cfg.WebAddress + "/dex"
	cfg.RedirectURI = "https://" + cfg.WebAddress + "/auth/callback"
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for _, file := range []struct {
		path string
		data []byte
	}{{cfg.TLSCertFile, certPEM}, {cfg.TLSKeyFile, keyPEM}, {cfg.GRPCCertFile, certPEM}, {cfg.GRPCKeyFile, keyPEM}, {cfg.GRPCClientCAFile, adminPEM}} {
		if err := os.WriteFile(file.path, file.data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(t.TempDir(), "dex.json")
	if err := identity.WriteDexConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	for _, listener := range reservations {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: configPath, HealthURL: "http://" + cfg.TelemetryAddress + "/healthz/ready", Client: &http.Client{Timeout: time.Second}, PollInterval: 20 * time.Millisecond, StartupTimeout: 5 * time.Second, ShutdownTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { defer close(finished); finished <- process.Run(ctx) }() // Cleanup cancels and joins the native child.
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("native Dex did not stop")
		}
	})
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(7 * time.Second)
	defer deadline.Stop()
	for !process.Healthy() {
		select {
		case err := <-finished:
			t.Fatalf("native Dex exited before readiness: %v", err)
		case <-deadline.C:
			t.Fatal("native Dex readiness timed out")
		case <-ticker.C:
		}
	}
	if _, err := identity.NewOIDC(ctx, cfg.Issuer, cfg.ClientID, client, time.Now); err != nil {
		t.Fatalf("discover native Dex over verified HTTPS: %v", err)
	}
	// Exercise the real authenticated administration API, including a setup retry
	// after the owner has already changed their initial credential.
	adminTLS := client.Transport.(*http.Transport).TLSClientConfig.Clone()
	adminTLS.ServerName = "127.0.0.1"
	adminTLS.Certificates = []tls.Certificate{adminCertificate}
	apiClient, connection, err := identity.NewDexAdminClient(cfg.GRPCAddress, adminTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// A separate client authority cannot use the privileged API, even on loopback.
	foreignCertificate, _ := adminCertificateFixture(t)
	foreignTLS := adminTLS.Clone()
	foreignTLS.Certificates = []tls.Certificate{foreignCertificate}
	foreignClient, foreignConnection, err := identity.NewDexAdminClient(cfg.GRPCAddress, foreignTLS)
	if err != nil {
		t.Fatal(err)
	}
	foreignCtx, cancelForeign := context.WithTimeout(ctx, time.Second)
	_, foreignErr := foreignClient.ListPasswords(foreignCtx, &api.ListPasswordReq{})
	cancelForeign()
	foreignConnection.Close()
	if foreignErr == nil {
		t.Fatal("untrusted client certificate accessed Dex administration")
	}
	adminCtx, cancelAdmin := context.WithTimeout(ctx, 5*time.Second)
	defer cancelAdmin()
	hash, err := bcrypt.GenerateFromPassword([]byte("initial-fixture-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	admin := identity.PasswordAdmin{Client: apiClient}
	if err := admin.Ensure(adminCtx, "owner@example.test", "stable-owner-id", hash); err != nil {
		t.Fatal(err)
	}
	changed, err := bcrypt.GenerateFromPassword([]byte("changed-fixture-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := apiClient.UpdatePassword(adminCtx, &api.UpdatePasswordReq{Email: "owner@example.test", NewHash: changed})
	if err != nil || updated.NotFound {
		t.Fatal("native Dex password update failed")
	}
	if err := admin.Ensure(adminCtx, "owner@example.test", "stable-owner-id", hash); err != nil {
		t.Fatal(err)
	}
	verified, err := apiClient.VerifyPassword(adminCtx, &api.VerifyPasswordReq{Email: "owner@example.test", Password: "changed-fixture-password"})
	if err != nil || !verified.Verified {
		t.Fatal("native Dex retry did not preserve changed password")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("native Dex shutdown = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native Dex shutdown timed out")
	}
}

// TestDexSupervisorReportsExitWithoutLeakingOutput fails closed when the managed identity process crashes.
func TestDexSupervisorReportsExitWithoutLeakingOutput(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dex-fixture")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Dex Version: v2.46.1\\n'; exit 0; fi\nprintf 'private-process-password' >&2\nexit 7\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: filepath.Join(t.TempDir(), "dex.json"), HealthURL: server.URL, Client: server.Client(), PollInterval: 5 * time.Millisecond, StartupTimeout: time.Second, ShutdownTimeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := process.Run(ctx)
	if !errors.Is(err, identity.ErrDexExited) || strings.Contains(err.Error(), "private-process-password") {
		t.Errorf("Run(crashed Dex) = %v, want safe process failure", err)
	}
	if process.Healthy() {
		t.Error("crashed identity process remains healthy")
	}
}

// TestDexSupervisorCancelsAndJoinsChild prevents the controller shutdown leaving its identity process running.
func TestDexSupervisorCancelsAndJoinsChild(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dex-fixture")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Dex Version: v2.46.1\\n'; exit 0; fi\nexec /bin/sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: filepath.Join(t.TempDir(), "dex.json"), HealthURL: server.URL, Client: server.Client(), PollInterval: 5 * time.Millisecond, StartupTimeout: time.Second, ShutdownTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- process.Run(ctx) }() // Run joins its child before this goroutine exits.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !process.Healthy() {
		select {
		case err := <-finished:
			t.Fatalf("process exited before ready: %v", err)
		case <-deadline.C:
			t.Fatal("identity process did not become ready")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run(cancelled) = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("managed identity child did not stop")
	}
	if process.Healthy() {
		t.Error("stopped identity process remains healthy")
	}
}

// TestDexSupervisorRejectsUnhealthyStartup avoids accepting sign-in against an unready identity store.
func TestDexSupervisorRejectsUnhealthyStartup(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dex-fixture")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Dex Version: v2.46.1\\n'; exit 0; fi\nexec /bin/sleep 60\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: filepath.Join(t.TempDir(), "dex.json"), HealthURL: server.URL, Client: server.Client(), PollInterval: 5 * time.Millisecond, StartupTimeout: 300 * time.Millisecond, ShutdownTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.Run(ctx); !errors.Is(err, identity.ErrDexUnhealthy) {
		t.Errorf("Run(unhealthy startup) = %v", err)
	}
}

func signedIdentityToken(t *testing.T, key *rsa.PrivateKey, issuer string, change func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{"iss": issuer, "sub": "dex-opaque-subject", "aud": "shiftbox", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": "login-nonce", "email": "same@example.test", "email_verified": true, "federated_claims": map[string]string{"connector_id": "company", "user_id": "stable-upstream-subject"}}
	if change != nil {
		change(claims)
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(encoded)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// TestOIDCUsesStableFederatedIdentity verifies signatures and binds to the configured upstream, not email.
func TestOIDCUsesStableFederatedIdentity(t *testing.T) {
	provider, key, issuer := oidcFixture(t)
	connector := identity.ConnectorIdentity{ID: "company", Issuer: "https://company.example.test"}
	first, err := provider.Verify(context.Background(), signedIdentityToken(t, key, issuer, nil), "login-nonce", connector)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Verify(context.Background(), signedIdentityToken(t, key, issuer, func(claims map[string]any) {
		claims["federated_claims"] = map[string]string{"connector_id": "company", "user_id": "different-subject"}
	}), "login-nonce", connector)
	if err != nil {
		t.Fatal(err)
	}
	if first.Issuer != connector.Issuer || first.Subject != "stable-upstream-subject" || first.ConnectorID != "company" {
		t.Errorf("verified identity = %+v", first)
	}
	if first.Subject == second.Subject || first.Email != second.Email {
		t.Error("same-email identities were merged or profile was lost")
	}
}

// TestOIDCRejectsInvalidClaims prevents nonce replay, connector confusion, and expired or foreign tokens.
func TestOIDCRejectsInvalidClaims(t *testing.T) {
	provider, key, issuer := oidcFixture(t)
	connector := identity.ConnectorIdentity{ID: "company", Issuer: "https://company.example.test"}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://attacker.test" }},
		{"wrong audience", func(c map[string]any) { c["aud"] = "other-app" }},
		{"wrong authorized party", func(c map[string]any) { c["aud"] = []string{"shiftbox", "other-app"}; c["azp"] = "other-app" }},
		{"ambiguous authorized party", func(c map[string]any) { c["aud"] = []string{"shiftbox", "other-app"} }},
		{"future issue time", func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() }},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }},
		{"nonce replay", func(c map[string]any) { c["nonce"] = "other-login" }},
		{"empty subject", func(c map[string]any) { c["sub"] = "" }},
		{"missing federation", func(c map[string]any) { delete(c, "federated_claims") }},
		{"wrong connector", func(c map[string]any) {
			c["federated_claims"] = map[string]string{"connector_id": "other", "user_id": "stable-upstream-subject"}
		}},
	} {
		// Each signed but ineligible claim set must fail the relying-party boundary.
		t.Run(test.name, func(t *testing.T) {
			if _, err := provider.Verify(context.Background(), signedIdentityToken(t, key, issuer, test.change), "login-nonce", connector); !errors.Is(err, identity.ErrInvalidIdentity) {
				t.Errorf("Verify(%s) = %v, want invalid identity", test.name, err)
			}
		})
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Verify(context.Background(), signedIdentityToken(t, other, issuer, nil), "login-nonce", connector); !errors.Is(err, identity.ErrInvalidIdentity) {
		t.Error("token signed by unknown key accepted")
	}
}

func validDexConfig(t *testing.T) identity.DexConfig {
	t.Helper()
	dir := t.TempDir()
	return identity.DexConfig{Issuer: "https://shiftbox.test/dex", WebAddress: "127.0.0.1:5556", TLSCertFile: filepath.Join(dir, "web.crt"), TLSKeyFile: filepath.Join(dir, "web.key"), TelemetryAddress: "127.0.0.1:5558", GRPCAddress: "127.0.0.1:5557", GRPCCertFile: filepath.Join(dir, "admin.crt"), GRPCKeyFile: filepath.Join(dir, "admin.key"), GRPCClientCAFile: filepath.Join(dir, "admin-client-ca.crt"), SQLitePath: filepath.Join(dir, "dex.db"), ClientID: "shiftbox", ClientSecret: "private-client-secret", RedirectURI: "https://shiftbox.test/auth/callback"}
}

// TestDexConfigProtectsCredentials writes managed configuration privately and requires authenticated local listeners.
func TestDexConfigProtectsCredentials(t *testing.T) {
	cfg := validDexConfig(t)
	path := filepath.Join(t.TempDir(), "dex.json")
	if err := os.WriteFile(path, []byte("old configuration"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := identity.WriteDexConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Dex config mode = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Web              map[string]any `json:"web"`
		GRPC             map[string]any `json:"grpc"`
		EnablePasswordDB bool           `json:"enablePasswordDB"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Web["https"] != cfg.WebAddress || document.Web["http"] != nil || document.GRPC["tlsClientCA"] != cfg.GRPCClientCAFile || !document.EnablePasswordDB {
		t.Errorf("unsafe managed Dex configuration: %+v", document)
	}
	for _, change := range []func(*identity.DexConfig){
		func(c *identity.DexConfig) { c.WebAddress = "0.0.0.0:5556" },
		func(c *identity.DexConfig) { c.GRPCClientCAFile = "" },
		func(c *identity.DexConfig) { c.Issuer = "http://shiftbox.test/dex" },
		func(c *identity.DexConfig) { c.SQLitePath = "relative.db" },
	} {
		invalid := cfg
		change(&invalid)
		if err := identity.WriteDexConfig(path, invalid); err == nil || strings.Contains(err.Error(), cfg.ClientSecret) {
			t.Error("invalid Dex config accepted or secret exposed")
		}
	}
}

func adminCertificateFixture(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
