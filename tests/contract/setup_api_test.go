package contract_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestBootstrapAccessBoundary prevents remote clients and DNS-rebound hosts from reaching protected setup, even with forged forwarding headers.
func TestBootstrapAccessBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, origin, host, remote string
		tls                        bool
		want                       int
	}{
		{"local IPv4", "https://127.0.0.1:8443", "127.0.0.1:8443", "127.0.0.1:32100", true, 204},
		{"local IPv6", "https://[::1]:8443", "[::1]:8443", "[::1]:32100", true, 204},
		{"remote peer", "https://127.0.0.1:8443", "127.0.0.1:8443", "192.0.2.4:32100", true, 403},
		{"DNS rebound host", "https://127.0.0.1:8443", "attacker.test:8443", "127.0.0.1:32100", true, 403},
		{"wrong port", "https://127.0.0.1:8443", "127.0.0.1:8444", "127.0.0.1:32100", true, 403},
		{"plaintext", "https://127.0.0.1:8443", "127.0.0.1:8443", "127.0.0.1:32100", false, 403},
		{"invalid peer", "https://127.0.0.1:8443", "127.0.0.1:8443", "unparseable", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler, err := app.ProtectBootstrap(tc.origin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(204) }))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", tc.origin+"/api/v1/setup/status", nil)
			r.Host = tc.host
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Forwarded-Host", "127.0.0.1:8443")
			r.TLS = nil
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || reached != (tc.want == 204) {
				t.Errorf("bootstrap %s status/reached=%d/%t, want %d/%t", tc.name, w.Code, reached, tc.want, tc.want == 204)
			}
		})
	}
}

// TestBootstrapRejectsUnsafeOrigin prevents composition from accidentally configuring a public or plaintext setup address.
func TestBootstrapRejectsUnsafeOrigin(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.1:8443", "https://0.0.0.0:8443", "https://192.0.2.1:8443", "https://localhost.attacker.test:8443", "https://127.0.0.1:8443/path", "https://127.0.0.1:8443?", "https://127.0.0.1:8443#", "https://127.0.0.1:65536"} {
		if _, err := app.ProtectBootstrap(origin, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
			t.Errorf("ProtectBootstrap(%q) accepted unsafe origin", origin)
		}
	}
}

// TestSetupAPILifecycle verifies that local setup delivers one temporary
// credential, resumes the same workflow, and retires bootstrap access on activation.
func TestSetupAPILifecycle(t *testing.T) {
	const origin = "https://127.0.0.1:8443"
	const management = "https://management.test"
	const ide = "{workspace}.ide.test"
	const preview = "{service}--{workspace}.preview.test"
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	seed := database.BootstrapSeed{ID: uuid.NewString(), Organization: "Fixture", Origin: origin, IDEPattern: ide,
		PreviewPattern: preview, Release: "fixture", MinGuestRelease: "fixture", Now: now}
	db, installation, err := database.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "bootstrap.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	certificate, roots := setupCertificateFixture(t, now)
	admin := &setupPasswordAdmin{}
	newHandler := func() http.Handler {
		t.Helper()
		handler, err := app.NewSetupHandler(app.SetupHTTP{
			DB: db, InstallationID: installation, Origin: origin, Cipher: cipher, Admin: admin,
			Login: &app.SetupLogin{}, CertificateRoots: roots, Journal: operation.NewJournal(db, clock, uuid.NewString),
			Now: clock, NewID: uuid.NewString,
		})
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	handler := newHandler()
	var token string
	request := func(method, route, key string, body any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, origin+route, bytes.NewReader(raw))
		r.RemoteAddr = "127.0.0.1:61520"
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if route == "/api/v1/admin/capacity" {
			r.Header.Set("If-Match", `"0"`)
		}
		if token != "" {
			cookie := httptest.NewRecorder()
			if err := web.SetSessionCookie(cookie, token, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			r.AddCookie(cookie.Result().Cookies()[0])
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d, want %d", method, route, w.Code, want)
		}
		return w
	}
	const administrator = "/api/v1/setup/administrator"
	const profile = "/api/v1/setup/data-profile"
	const activate = "/api/v1/setup/activate"
	const administratorRequest = "setup-contract-admin"
	const activationRequest = "setup-contract-activate"
	email := map[string]string{"email": "owner@example.test"}
	issued := request(http.MethodPost, administrator, administratorRequest, email, http.StatusCreated)
	var delivery struct {
		Password string `json:"temporaryPassword"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &delivery); err != nil || len(delivery.Password) < 24 || issued.Header().Get("Cache-Control") != "no-store" || admin.userID == "" {
		t.Fatal("local setup did not deliver a single non-cacheable temporary password")
	}
	handler = newHandler()
	replay := request(http.MethodPost, administrator, administratorRequest, email, http.StatusConflict)
	status := request(http.MethodGet, "/api/v1/setup/status", "", nil, http.StatusOK)
	if bytes.Contains(replay.Body.Bytes(), []byte(delivery.Password)) || bytes.Contains(status.Body.Bytes(), []byte(delivery.Password)) ||
		!bytes.Contains(status.Body.Bytes(), []byte(`"availableSteps":["password"]`)) {
		t.Fatal("setup restart did not safely retain the password-change step")
	}
	// Seed the session that Dex would issue. The CLI contract tests perform
	// the actual sign-in and password-change exchange with the pinned Dex.
	session, sessionToken, err := auth.NewSession(uuid.NewString(), admin.userID, "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	token = sessionToken
	request(http.MethodPut, profile, "setup-contract-profile", map[string]string{"profile": "sqlite"}, http.StatusForbidden)
	if _, err := db.ExecContext(t.Context(), "UPDATE users SET must_change_password=0 WHERE id=$1", admin.userID); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, activate, activationRequest, nil, http.StatusConflict)
	const privateInput = "fixture-private-profile-input"
	invalidProfile := request(http.MethodPut, profile, "setup-contract-profile", map[string]string{"profile": "sqlite", "connectionUri": privateInput}, http.StatusBadRequest)
	if strings.Contains(invalidProfile.Body.String(), privateInput) {
		t.Fatal("invalid profile response disclosed its input")
	}
	request(http.MethodPut, profile, "setup-contract-profile", map[string]string{"profile": "sqlite"}, http.StatusAccepted)
	certificate["managementOrigin"], certificate["ideHostPattern"], certificate["previewHostPattern"] = management, ide, preview
	keyPEM := certificate["privateKeyPem"]
	certificate["privateKeyPem"] = "fixture-invalid-private-key-sentinel"
	invalidCertificate := request(http.MethodPut, "/api/v1/setup/certificate", "setup-contract-cert", certificate, http.StatusBadRequest)
	if strings.Contains(invalidCertificate.Body.String(), certificate["privateKeyPem"]) {
		t.Fatal("invalid certificate response disclosed its key input")
	}
	certificate["privateKeyPem"] = keyPEM
	request(http.MethodPut, "/api/v1/setup/certificate", "setup-contract-cert", certificate, http.StatusAccepted)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,is_default)
		VALUES('fixture','fixture','amd64','fixture','fixture','1','[]','{}','{}','{}',$1,'fixture.utm',$2,$3,'available',1)`,
		strings.Repeat("a", 64), admin.userID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// A fresh installation accepts the UI's blank defaults as unlimited budgets.
	request(http.MethodPut, "/api/v1/admin/capacity", "", map[string]int{"cpuCount": 0, "memoryMiB": 0, "diskMiB": 0}, http.StatusNoContent)
	completed := request(http.MethodPost, activate, activationRequest, nil, http.StatusAccepted)
	cookies := completed.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatal("activation did not clear the bootstrap browser cookie")
	}
	request(http.MethodGet, "/api/v1/session", "", nil, http.StatusUnauthorized)
	request(http.MethodPost, activate, activationRequest, nil, http.StatusUnauthorized)
	var state, revocation string
	if err := db.QueryRowContext(t.Context(), `SELECT i.state,s.revocation_reason FROM installations i CROSS JOIN sessions s
		WHERE i.id=$1 AND s.id=$2 AND s.revoked_at IS NOT NULL`, installation, session.ID).Scan(&state, &revocation); err != nil || state != "active" || revocation != "setup_activated" {
		t.Fatalf("activation state/revocation = %q/%q, %v; want active/setup_activated", state, revocation, err)
	}
	var cpu, memory, disk int64
	if err := db.QueryRowContext(t.Context(), "SELECT cpu_limit,memory_mib_limit,disk_mib_limit FROM capacity_budgets").
		Scan(&cpu, &memory, &disk); err != nil || cpu != 0 || memory != 0 || disk != 0 {
		t.Errorf("fresh activated capacity = %d/%d/%d (%v), want all Unlimited", cpu, memory, disk, err)
	}
}

type setupPasswordAdmin struct{ userID string }

func (f *setupPasswordAdmin) Ensure(_ context.Context, _, id string, _ []byte) error {
	f.userID = id
	return nil
}

func setupCertificateFixture(t *testing.T, now time.Time) (map[string]string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{DNSNames: []string{"management.test", "*.ide.test", "*.preview.test", "*.repair.test"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemCertificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemCertificate) {
		t.Fatal("cannot trust the generated setup certificate")
	}
	return map[string]string{
		"certificateChainPem": string(pemCertificate),
		"privateKeyPem":       string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}, roots
}
