package integration_test

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestSecretRedactionAcrossManagementAndBackup follows a synthetic application
// secret from a write-only API request through the audit log and a real archive.
func TestSecretRedactionAcrossManagementAndBackup(t *testing.T) {
	ctx, dir, now := t.Context(), t.TempDir(), time.Now()
	const origin = "https://management.test"
	installation := uuid.NewString()
	db, _, err := database.OpenBootstrap(ctx, filepath.Join(dir, "app.db"), database.BootstrapSeed{
		ID: installation, Organization: "Redaction fixture", Origin: origin,
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	owner, token, value, passphrase := uuid.NewString(), redactionCanary(t), redactionCanary(t), redactionCanary(t)
	csrf := auth.CSRFToken(token)
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation); err != nil {
			return err
		}
		if _, err := store.CreateUser(ctx, tx, auth.User{ID: owner, PrimaryEmail: "redaction@example.test",
			Role: "administrator", MembershipState: "active", CredentialMode: "local"}, "fixture", now); err != nil {
			return err
		}
		return store.SaveSession(ctx, tx, auth.Session{
			ID: uuid.NewString(), UserID: owner, Mode: "normal", TokenDigest: auth.TokenDigest(token),
			CSRFDigest: auth.TokenDigest(csrf), AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
	}); err != nil {
		t.Fatal(err)
	}

	clock := func() time.Time { return now }
	mux := http.NewServeMux()
	for _, handler := range web.SecretHandlers(db, origin, secret.NewService(cipher), clock) {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, &handler)
	}
	auditHandler := web.ListAuditEventsHandler(db, origin, clock)
	mux.Handle("GET /api/v1/admin/audit", &auditHandler)
	var log bytes.Buffer
	handler := web.WithRequestID(web.WithRequestLogging(slog.New(slog.NewJSONHandler(&log, nil)), mux))
	request := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("X-Request-ID", value)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned status %d, want %d", method, w.Code, want)
		}
		var headers bytes.Buffer
		if err := w.Header().Write(&headers); err != nil {
			t.Fatal("cannot inspect management response headers")
		}
		assertRedacted(t, "management response body and headers", w.Body.String()+headers.String(), value, token, csrf)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s response did not use no-store", method)
		}
		return w.Body.String()
	}
	writePath := "/api/v1/secrets/user/" + owner + "/REDACTION_PROBE"
	request(http.MethodPut, writePath+"?probe="+url.QueryEscape(value), `{"value":"`+value+`"}`, http.StatusNoContent)
	request(http.MethodPut, writePath, `{"unexpected":"`+value+`"}`, http.StatusBadRequest)
	metadata := request(http.MethodGet, "/api/v1/secrets?scope=user&scopeId="+owner, "", http.StatusOK)
	auditJSON := request(http.MethodGet, "/api/v1/admin/audit", "", http.StatusOK)
	if !strings.Contains(metadata, "REDACTION_PROBE") || !strings.Contains(auditJSON, "value_saved") {
		t.Fatal("fixture did not return saved metadata and its audit event")
	}
	assertRedacted(t, "structured request log", log.String(), value, token, csrf)
	if logLines := bytes.Count(log.Bytes(), []byte("\n")); logLines != 4 || !bytes.Contains(log.Bytes(), []byte("/api/v1/secrets/{scope}/{scopeId}/{name}")) {
		t.Fatalf("request logging produced %d entries, want four entries with the route template", logLines)
	}

	var auditMetadata, encrypted string
	if err := db.QueryRowContext(ctx, "SELECT metadata_json FROM audit_events WHERE reason_code='value_saved'").Scan(&auditMetadata); err != nil || auditMetadata != "{}" {
		t.Fatal("secret write transaction failed to save bounded audit metadata")
	}
	if err := db.QueryRowContext(ctx, "SELECT ciphertext_json FROM secret_values WHERE name='REDACTION_PROBE'").Scan(&encrypted); err != nil || encrypted == "" {
		t.Fatal("secret write transaction failed to save ciphertext")
	}
	assertRedacted(t, "persisted secret record", encrypted, value, token, csrf)
	// The schema must reject unknown credential-bearing fields independently of
	// the Go metadata type.
	if _, err := db.ExecContext(ctx, `UPDATE audit_events SET metadata_json=$1 WHERE reason_code='value_saved'`,
		`{"authorization":"`+value+`"}`); err == nil {
		t.Error("audit schema accepted credential-bearing metadata")
	}

	schema, err := database.CurrentSchemaVersion("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "fixture.age")
	checkpoint := filepath.Join(dir, "checkpoint.db")
	if err := backup.SQLiteCheckpoint(ctx, db, checkpoint); err != nil {
		t.Fatal(err)
	}
	placeholder := filepath.Join(dir, "other-input")
	if err := os.WriteFile(placeholder, []byte("fixture-only input"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = backup.CreateArchive(ctx, archive, passphrase, backup.ArchiveMetadata{
		InstallationID: installation, Release: "test", Profile: "sqlite", Schema: schema,
	}, map[string]string{
		"application.db": checkpoint, "dex.db": placeholder, "configuration.json": placeholder,
		"installation.key": placeholder, "guest-ca.json": placeholder,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRedactedArchive(t, archive, passphrase, encrypted, value, token, csrf)
}

func checkRedactedArchive(t *testing.T, path, passphrase, encrypted string, canaries ...string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	protected := append(canaries, passphrase)
	assertRedacted(t, "published encrypted archive", string(raw), protected...)
	archiveIdentity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := age.Decrypt(bytes.NewReader(raw), archiveIdentity)
	if err != nil {
		t.Fatal(err)
	}
	entries := tar.NewReader(reader)
	found := false
	for {
		entry, err := entries.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || entry.Size > 2<<20 {
			t.Fatal("cannot inspect bounded fixture archive")
		}
		data, err := io.ReadAll(entries)
		if err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, "archive entry names and contents", entry.Name+string(data), protected...)
		if entry.Name == "application.db" {
			found = bytes.Contains(data, []byte(encrypted))
		}
	}
	if !found {
		t.Error("restore payload omitted the encrypted application secret")
	}
}

// TestServiceEntryKeepsCredentialsAtTheirOrigins checks the actual one-use
// entry URL, scrubbed diagnostics, and the authenticated forwarding boundary.
func TestServiceEntryKeepsCredentialsAtTheirOrigins(t *testing.T) {
	f := newRevocationFixture(t)
	f.exec(t, "UPDATE workspaces SET preview_ports_json='[8080]' WHERE id=$1", f.ref.WorkspaceID)
	hosts, err := config.NewServiceHosts(revocationManagementOrigin, revocationIDEPattern, revocationPreviewPattern)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return f.now }
	entry := web.WorkspaceEntryGrantHandler(f.db, revocationManagementOrigin, clock, hosts)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/workspaces/{workspaceId}/entry-grants", entry)
	var diagnostic bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&diagnostic, nil))
	request := httptest.NewRequest(http.MethodPost, revocationManagementOrigin+"/api/v1/workspaces/"+f.ref.WorkspaceID+"/entry-grants",
		strings.NewReader(`{"service":"preview-8080"}`))
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: f.ownerToken})
	request.Header.Set("Origin", revocationManagementOrigin)
	csrf := auth.CSRFToken(f.ownerToken)
	request.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	web.WithRequestLogging(logger, mux).ServeHTTP(response, request)
	var issued struct {
		ExchangeURL string `json:"exchangeUrl"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &issued) != nil {
		t.Fatalf("preview entry grant status = %d, want 201 with a one-use URL", response.Code)
	}
	exchange, err := url.Parse(issued.ExchangeURL)
	if err != nil {
		t.Fatal("cannot read issued exchange URL")
	}
	grant := exchange.Query().Get("grant")
	if exchange.Scheme != "https" || exchange.Hostname() == request.URL.Hostname() || grant == "" || len(exchange.Query()) != 1 {
		t.Fatal("the exchange URL must scope a single grant to the HTTPS preview origin")
	}
	assertRedacted(t, "issued preview URL", issued.ExchangeURL, f.ownerToken, csrf)

	forwarded := false
	service := web.WithRequestLogging(logger, web.ServiceProxyHandler(f.db, hosts, f.tunnels.Registry, clock,
		func(w http.ResponseWriter, r *http.Request, _ string, _ int64, _, _ string) {
			forwarded = true
			assertRedacted(t, "forwarded workspace request", r.Header.Get("Cookie")+r.URL.String(), f.ownerToken, grant)
			if selected, err := r.Cookie("fixture-preference"); err != nil || selected.Value != "preserved" {
				t.Error("forwarding did not preserve the ordinary application cookie")
			}
			if _, err := r.Cookie("__Host-shiftbox_origin"); !errors.Is(err, http.ErrNoCookie) {
				t.Error("forwarded request retained the service authority cookie")
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	open := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, issued.ExchangeURL, nil))
		return response
	}
	exchanged := open()
	assertRedacted(t, "preview exchange body and destination", exchanged.Body.String()+exchanged.Header().Get("Location"), grant, f.ownerToken, csrf)
	cookies := exchanged.Result().Cookies()
	if exchanged.Code != http.StatusOK || exchanged.Header().Get("Cache-Control") != "no-store" ||
		exchanged.Header().Get("Referrer-Policy") != "no-referrer" || len(cookies) != 1 ||
		!cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Domain != "" || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("entry exchange must use no-store/no-referrer and one host-only, secure HttpOnly cookie")
	}
	if replay := open(); replay.Code != http.StatusForbidden {
		t.Errorf("preview exchange replay status = %d, want 403", replay.Code)
	}

	preview := httptest.NewRequest(http.MethodGet, "https://"+exchange.Host+"/", nil)
	preview.AddCookie(cookies[0])
	preview.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: f.ownerToken})
	preview.AddCookie(&http.Cookie{Name: "fixture-preference", Value: "preserved"})
	result := httptest.NewRecorder()
	service.ServeHTTP(result, preview)
	if !forwarded || result.Code != http.StatusNoContent {
		t.Errorf("preview forwarding = %t, status = %d, want authorized forwarding and 204", forwarded, result.Code)
	}
	assertRedacted(t, "preview entry diagnostics", diagnostic.String(), f.ownerToken, csrf, grant, cookies[0].Value)
}

// TestDexCredentialsStayOutOfProcessArguments checks the real supervisor's
// process boundary with a fixture child that records only arguments and env.
func TestDexCredentialsStayOutOfProcessArguments(t *testing.T) {
	dir := t.TempDir()
	clientSecret, databasePassword := redactionCanary(t), redactionCanary(t)
	configPath := filepath.Join(dir, "dex.json")
	config := identity.DexConfig{
		Issuer: "https://management.test/dex", RedirectURI: "https://management.test/auth/callback",
		ClientID: "shiftbox", ClientSecret: clientSecret, TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile: filepath.Join(dir, "tls.key"), GRPCCertFile: filepath.Join(dir, "grpc.pem"),
		GRPCKeyFile: filepath.Join(dir, "grpc.key"), GRPCClientCAFile: filepath.Join(dir, "grpc-ca.pem"),
		WebAddress: "127.0.0.1:5511", TelemetryAddress: "127.0.0.1:5512", GRPCAddress: "127.0.0.1:5513",
		Postgres: &identity.DexPostgres{
			Host: "localhost", Port: 5432, Database: "fixture", User: "fixture", Password: databasePassword,
		},
	}
	if err := identity.WriteDexConfig(configPath, config); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	info, statErr := os.Stat(configPath)
	if err != nil || statErr != nil || info.Mode().Perm() != 0600 ||
		!bytes.Contains(raw, []byte(clientSecret)) || !bytes.Contains(raw, []byte(databasePassword)) {
		t.Fatal("protected Dex configuration did not contain both synthetic credentials")
	}

	self := filepath.Join(dir, "fake-dex")
	script := `#!/bin/sh
if [ "$1" = version ]; then printf 'Dex Version: %s\n' '` + identity.DexVersion + `'; exit 0; fi
{ printf '%s\n' "$@"; /usr/bin/env; } > "${0%/*}/invocation"
`
	if err := os.WriteFile(self, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSWORD", databasePassword)
	t.Setenv("SHIFTBOX_FIXTURE_CLIENT_SECRET", clientSecret)
	transport := redactionRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("healthy"))}, nil
	})
	dex := identity.DexProcess{
		BinaryPath: self, ConfigPath: configPath, HealthURL: "http://127.0.0.1:5512/health",
		Client: &http.Client{Transport: transport}, StartupTimeout: 5 * time.Second,
	}
	if err := dex.Run(t.Context()); !errors.Is(err, identity.ErrDexExited) {
		t.Fatalf("Dex fixture process result = %v, want controlled child exit", err)
	}
	invocation, err := os.ReadFile(filepath.Join(dir, "invocation"))
	if err != nil || !bytes.HasPrefix(invocation, []byte("serve\n"+configPath+"\n")) {
		t.Fatal("Dex fixture did not capture the actual supervised invocation")
	}
	assertRedacted(t, "managed identity child arguments and environment", string(invocation), clientSecret, databasePassword)
}

type redactionRoundTripper func(*http.Request) (*http.Response, error)

func (f redactionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func redactionCanary(t *testing.T) string {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func assertRedacted(t *testing.T, boundary, observed string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(observed, canary) {
			t.Errorf("synthetic credential appeared in %s", boundary)
		}
	}
}
