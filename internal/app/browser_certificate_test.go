package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestReplaceBrowserCertificate checks administrator authority, rollback on
// validation or journal failure, and durable idempotent replacement.
func TestReplaceBrowserCertificate(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	var clockTime atomic.Pointer[time.Time]
	clockTime.Store(&now)
	clock := func() time.Time { return *clockTime.Load() }
	const origin = "https://management.test"
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "installation.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin,
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	newSession := func(role string) (string, string) {
		t.Helper()
		user := uuid.NewString()
		session, token, err := auth.NewSession(uuid.NewString(), user, "normal", now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
				VALUES($1,$2,$3,'active','local','setup','now','now',1)`, user, role+"@test.invalid", role)
			if err != nil {
				return err
			}
			return auth.NewStore(db).SaveSession(ctx, tx, session)
		}); err != nil {
			t.Fatal(err)
		}
		return user, token
	}
	admin, token := newSession("administrator")
	_, memberToken := newSession("member")
	roots := x509.NewCertPool()
	old := browserCertificateFixture(t, roots, now, time.Hour)
	replacement := browserCertificateFixture(t, roots, now, 90*24*time.Hour)
	settings := config.InstallationSettings{ManagementOrigin: origin, OrganizationName: "ShiftBox", DataProfile: "sqlite",
		IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := config.ActivateBrowserCertificate(ctx, tx, config.Activation{InstallationID: id, ActorID: admin, ActivatedAt: now}, settings, old, cipher, roots)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	initial, err := config.LoadBrowserCertificate(ctx, db, id, old.ID, cipher, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	guestDir := t.TempDir()
	guestCA, err := EnsureInstallationCA(guestDir, id, cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	controller := &ControllerState{DB: db, BrowserTLS: initial, GuestCA: guestCA, Installation: ActiveInstallation{InstallationID: id, Generation: 1, Settings: settings},
		cipher: cipher, browserRoots: roots}
	server, err := ListenBrowser("127.0.0.1:0", controller, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure fixture response")
	}), &proxy.Tunnels{Registry: &proxy.Registry{}}, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(runCtx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	dial := func() (*tls.Conn, error) {
		dialer := tls.Dialer{Config: &tls.Config{ServerName: "management.test", RootCAs: roots, MinVersion: tls.VersionTLS13}}
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := dialer.DialContext(dialCtx, "tcp", server.Address)
		if err != nil {
			return nil, err
		}
		return conn.(*tls.Conn), nil
	}
	handler := controller.browserCertificateHandler(operation.NewJournal(db, clock, uuid.NewString), clock)
	// Metadata follows the certificate selected by the listener, including after
	// a committed replacement; neither members nor anonymous callers may read it.
	readCertificate := func(sessionToken string, wantStatus int, want *x509.Certificate) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, origin+"/api/v1/admin/certificate", nil)
		if sessionToken != "" {
			r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: sessionToken})
		}
		w := httptest.NewRecorder()
		reader := controller.browserCertificateReadHandler(clock)
		reader.ServeHTTP(w, r)
		if w.Code != wantStatus {
			t.Fatalf("read certificate = HTTP %d, want %d: %s", w.Code, wantStatus, w.Body.String())
		}
		if want == nil {
			return
		}
		var got struct {
			Subject, Issuer, SerialNumber, FingerprintSHA256, SignatureAlgorithm, PublicKeyAlgorithm, Status string
			DNSNames                                                                                         []string
			NotBefore, NotAfter                                                                              time.Time
			Version, ChainLength                                                                             int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Subject != want.Subject.String() || got.Issuer != want.Issuer.String() ||
			got.SerialNumber != want.SerialNumber.Text(16) || got.FingerprintSHA256 != fmt.Sprintf("%x", sha256.Sum256(want.Raw)) ||
			strings.Join(got.DNSNames, ",") != strings.Join(want.DNSNames, ",") ||
			!got.NotBefore.Equal(want.NotBefore) || !got.NotAfter.Equal(want.NotAfter) ||
			got.SignatureAlgorithm != want.SignatureAlgorithm.String() || got.PublicKeyAlgorithm != want.PublicKeyAlgorithm.String() ||
			got.Version != want.Version || got.ChainLength != 1 {
			t.Errorf("read certificate = %+v, want public metadata for %s", got, want.SerialNumber)
		}
		wantState := "valid"
		if want.NotAfter.Sub(now) <= 30*24*time.Hour {
			wantState = "expiring"
		}
		if got.Status != wantState || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), "privateKey") {
			t.Errorf("read certificate status = %q, want %q without private material", got.Status, wantState)
		}
	}
	readCertificate("", 401, nil)
	readCertificate(memberToken, 403, nil)
	readCertificate(token, 200, initial.Leaf)
	served := func(want tls.Certificate) {
		t.Helper()
		conn, err := dial()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if leaf := conn.ConnectionState().PeerCertificates[0]; !bytes.Equal(leaf.Raw, want.Certificate[0]) {
			t.Errorf("listener certificate expires %v, want replacement leaf", leaf.NotAfter)
		}
	}
	send := func(token, key string, input config.BrowserCertificateInput, want int) string {
		t.Helper()
		body, err := json.Marshal(map[string]string{"certificateChainPem": string(input.ChainPEM), "privateKeyPem": string(input.KeyPEM)})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPut, origin+"/api/v1/admin/certificate", strings.NewReader(string(body)))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatalf("replace certificate = status %d, want %d without private material", w.Code, want)
		}
		return w.Body.String()
	}
	verify := func(generation int, chain []byte) {
		t.Helper()
		var got int
		var active string
		if err := db.QueryRowContext(ctx, `SELECT config_generation,(SELECT chain_pem FROM browser_certificates WHERE state='active')
			FROM installations WHERE id=$1`, id).Scan(&got, &active); err != nil || got != generation || active != string(chain) {
			t.Fatalf("active certificate generation = %d, %v, want %d with expected chain", got, err, generation)
		}
	}
	const requestKey = "replace-browser-certificate"
	send(memberToken, requestKey, replacement, 403)
	mismatch := replacement
	mismatch.KeyPEM = old.KeyPEM
	send(token, requestKey, mismatch, 400)
	verify(1, old.ChainPEM)
	served(initial)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_certificate_operation BEFORE INSERT ON operations
		BEGIN SELECT RAISE(ABORT, 'fixture journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	send(token, requestKey, replacement, 500)
	verify(1, old.ChainPEM)
	served(initial)
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_certificate_operation"); err != nil {
		t.Fatal(err)
	}
	accepted := send(token, requestKey, replacement, 202)
	var op operation.Operation
	if err := json.Unmarshal([]byte(accepted), &op); err != nil || op.ID == "" || op.Status != "succeeded" {
		t.Fatalf("certificate operation = %s (%v), want durable success", op.Status, err)
	}
	verify(2, replacement.ChainPEM)
	pair, err := tls.X509KeyPair(replacement.ChainPEM, replacement.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := LoadInstallationCA(guestDir, id, cipher, now)
	if err != nil || !bytes.Equal(retained.Certificate.Raw, guestCA.Certificate.Raw) ||
		bytes.Equal(retained.Certificate.RawSubjectPublicKeyInfo, pair.Leaf.RawSubjectPublicKeyInfo) {
		t.Fatalf("browser certificate replacement changed the separate guest CA or reused its key: %v", err)
	}
	served(pair)
	readCertificate(token, 200, pair.Leaf)
	if retry := send(token, requestKey, replacement, 202); retry != accepted {
		t.Fatal("certificate retry returned different operation evidence")
	}
	verify(2, replacement.ChainPEM)
	expired := now.Add(91 * 24 * time.Hour)
	clockTime.Store(&expired)
	if conn, err := dial(); err == nil {
		conn.Close()
		t.Fatal("listener accepted a fresh TLS connection after certificate expiry")
	}
	plaintext := &http.Client{Timeout: 3 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+server.Address, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "management.test"
	if response, err := plaintext.Do(request); err == nil {
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode < http.StatusBadRequest || bytes.Contains(body, []byte("secure fixture response")) {
			t.Errorf("plaintext after expiry = HTTP %d (%v), want rejection before reaching management", response.StatusCode, err)
		}
	}
}

// TestBrowserCertificateDetails keeps expired and not-yet-valid certificates
// inspectable and reports every SAN type without requiring private key access.
func TestBrowserCertificateDetails(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	uri, err := url.Parse("https://management.test/certificate")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		start, end time.Time
		status     string
	}{
		{name: "valid", start: now, end: now.Add(31 * 24 * time.Hour), status: "valid"},
		{name: "thirty day warning", start: now, end: now.Add(30 * 24 * time.Hour), status: "expiring"},
		{name: "expired", start: now.Add(-time.Hour), end: now, status: "expired"},
		{name: "not yet valid", start: now.Add(time.Hour), end: now.Add(31 * 24 * time.Hour), status: "not_yet_valid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf := &x509.Certificate{NotBefore: tc.start, NotAfter: tc.end,
				SerialNumber: big.NewInt(42), IPAddresses: []net.IP{net.ParseIP("192.0.2.1")},
				EmailAddresses: []string{"admin@example.test"}, URIs: []*url.URL{uri}}
			state := &ControllerState{BrowserTLS: tls.Certificate{Certificate: [][]byte{{1}}, Leaf: leaf}}
			handler, err := state.browserCertificateReadHandler(func() time.Time { return now }).Apply(
				t.Context(), nil, nil, auth.Principal{}, web.AuthorizationTarget{}, auth.Decision{})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			var got struct {
				Status                            string
				IPAddresses, EmailAddresses, URIs []string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.status || strings.Join(got.IPAddresses, ",") != "192.0.2.1" ||
				strings.Join(got.EmailAddresses, ",") != "admin@example.test" || strings.Join(got.URIs, ",") != uri.String() {
				t.Errorf("certificate details = %+v, want status %s with all SAN types", got, tc.status)
			}
		})
	}
}

func browserCertificateFixture(t *testing.T, roots *x509.CertPool, now time.Time, lifetime time.Duration) config.BrowserCertificateInput {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime),
		DNSNames: []string{"management.test", "*.ide.test", "*.preview.test", "*.repair.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(leaf)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return config.BrowserCertificateInput{ID: uuid.NewString(),
		ChainPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}
}
