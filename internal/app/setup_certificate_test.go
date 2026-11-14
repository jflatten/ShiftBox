package app

import (
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
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
	_ "modernc.org/sqlite"
)

// TestSetupCertificateUpload verifies the profile gate, failed validation rollback,
// encrypted key storage, and idempotent completion through the loopback API.
func TestSetupCertificateUpload(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	origin := "https://127.0.0.1:8443"
	seed := database.BootstrapSeed{ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin,
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "development", MinGuestRelease: "development", Now: now}
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "bootstrap.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	session, token, err := auth.NewSession(uuid.NewString(), owner, "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := PrepareAdministrator(ctx, tx, AdministratorIntent{InstallationID: id, UserID: owner, Email: "owner@example.test", CreatedAt: now}, cipher); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET must_change_password=0 WHERE id=$1", owner); err != nil {
			return err
		}
		return auth.NewStore(db).SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		DNSNames: []string{"management.test", "*.ide.test", "*.preview.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	input := map[string]string{
		"certificateChainPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		"privateKeyPem":       privatePEM, "managementOrigin": "https://management.test",
		"ideHostPattern": seed.IDEPattern, "previewHostPattern": seed.PreviewPattern,
	}
	setup := SetupHTTP{DB: db, InstallationID: id, Origin: origin, Cipher: cipher, CertificateRoots: roots,
		Now: func() time.Time { return now }, NewID: uuid.NewString, Journal: operation.NewJournal(db, time.Now, uuid.NewString)}
	mux := http.NewServeMux()
	mux.Handle("PUT /profile", setup.profileHandler())
	mux.Handle("PUT /certificate", setup.certificateHandler())
	handler, err := ProtectBootstrap(origin, mux)
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, key string, body any, want int) string {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("PUT", origin+path, strings.NewReader(string(payload)))
		r.RemoteAddr = "127.0.0.1:61520"
		r.TLS = &tls.ConnectionState{}
		cookie := httptest.NewRecorder()
		if err := web.SetSessionCookie(cookie, token, session.ExpiresAt); err != nil {
			t.Fatal(err)
		}
		r.AddCookie(cookie.Result().Cookies()[0])
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatalf("%s status = %d, want %d, with no private key in response", path, w.Code, want)
		}
		return w.Body.String()
	}
	request("/certificate", "setup-cert-upload-001", input, 409)
	request("/profile", "setup-profile-select-1", map[string]string{"profile": "sqlite"}, 202)
	input["privateKeyPem"] = "malformed private key"
	request("/certificate", "setup-cert-upload-001", input, 400)
	input["privateKeyPem"] = privatePEM
	first := request("/certificate", "setup-cert-upload-001", input, 202)
	if retry := request("/certificate", "setup-cert-upload-001", input, 202); first != retry {
		t.Fatal("certificate replay changed operation")
	}
	var generation int
	var state, encrypted string
	if err := db.QueryRow("SELECT state,config_generation FROM installations").Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "configuring" || generation != 1 {
		t.Fatalf("certificate upload produced unexpected installation state %s/%d", state, generation)
	}
	if err := db.QueryRow("SELECT p.value_json FROM protected_values p JOIN browser_certificates c ON p.id=c.private_key_ciphertext_id").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, "PRIVATE KEY") {
		t.Fatal("key stored unencrypted")
	}
}
