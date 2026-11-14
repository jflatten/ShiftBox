package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	_ "modernc.org/sqlite"
)

// TestPostgresSetupPreflight checks verified connections to two isolated schema
// owners and rejects invalid trust, misplaced search paths, shared logins, and occupied schemas.
func TestPostgresSetupPreflight(t *testing.T) {
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_SETUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI")
	if appURI == "" || dexURI == "" {
		t.Skip("set the two scoped setup fixture URIs")
	}
	cert, err := os.ReadFile(os.Getenv("SHIFTBOX_TEST_SETUP_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("fixture CA could not be loaded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := database.PostgresSetupTarget{ApplicationURI: appURI, DexURI: dexURI, Roots: roots}
	if err := database.PreflightPostgresSetup(ctx, target); err != nil {
		t.Fatal(err)
	}
	// A supplied connection URI cannot override TLS or introduce file lookups.
	for _, suffix := range []string{"&sslmode=disable", "&servicefile=/tmp/unsafe", "&host=other.test"} {
		unsafe := target
		unsafe.ApplicationURI += suffix
		if err := database.PreflightPostgresSetup(ctx, unsafe); err == nil || strings.Contains(err.Error(), appURI) {
			t.Error("unsafe connection parameters accepted or connection URI exposed")
		}
	}
	for _, change := range []func(*database.PostgresSetupTarget){
		func(target *database.PostgresSetupTarget) { target.Roots = x509.NewCertPool() },
		func(target *database.PostgresSetupTarget) { target.DexURI = target.ApplicationURI },
		func(target *database.PostgresSetupTarget) {
			dex, err := url.Parse(target.DexURI)
			if err != nil {
				t.Fatal("invalid fixture URI")
			}
			q := dex.Query()
			q.Set("search_path", "shiftbox")
			dex.RawQuery = q.Encode()
			target.DexURI = dex.String()
		},
	} {
		unsafe := target
		change(&unsafe)
		if err := database.PreflightPostgresSetup(ctx, unsafe); err == nil {
			t.Error("unsafe setup target passed preflight")
		}
	}
	parsed, err := url.Parse(appURI)
	if err != nil {
		t.Fatal("invalid fixture URI")
	}
	query := parsed.Query()
	query.Set("sslrootcert", os.Getenv("SHIFTBOX_TEST_SETUP_CA"))
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal("cannot open fixture application schema")
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("cannot begin fixture transaction")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE shiftbox.preflight_occupied_test (id integer)"); err != nil {
		t.Fatal("cannot create fixture table")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("cannot commit fixture table")
	}
	defer db.Exec("DROP TABLE shiftbox.preflight_occupied_test")
	if err := database.PreflightPostgresSetup(ctx, target); err == nil {
		t.Error("occupied target passed setup preflight")
	}

}

const activationCertificateID = "a45c934c-20c5-4046-abb8-41fee8f59a91"

func configureActivationCertificate(t *testing.T, db *sql.DB, cipher *cryptostore.Store, seed database.BootstrapSeed, now time.Time) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		DNSNames:    []string{"management.test", "*.ide.test", "*.preview.test"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(chain) {
		t.Fatal("failed to trust fixture certificate")
	}
	ctx := t.Context()
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := config.ActivateBrowserCertificate(ctx, tx,
			config.Activation{InstallationID: seed.ID, ActorID: "owner", ActivatedAt: now},
			config.InstallationSettings{OrganizationName: seed.Organization, DataProfile: "sqlite", ManagementOrigin: seed.Origin,
				IDEHostPattern: seed.IDEPattern, PreviewHostPattern: seed.PreviewPattern, ExternalAuthIntervalSeconds: 14400},
			config.BrowserCertificateInput{ID: activationCertificateID, ChainPEM: chain,
				KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}, cipher, roots)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return roots
}

// TestStagePostgresSetup copies only the initial account and current TLS
// configuration, and keeps the destination inactive and free of old sessions.
func TestStagePostgresSetup(t *testing.T) {
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_SETUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI")
	if appURI == "" || dexURI == "" {
		t.Skip("set the two scoped setup fixture URIs")
	}
	cert, err := os.ReadFile(os.Getenv("SHIFTBOX_TEST_SETUP_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("invalid fixture trust")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target := database.PostgresSetupTarget{ApplicationURI: appURI, DexURI: dexURI, Roots: roots}
	if err := database.PreflightPostgresSetup(ctx, target); err != nil {
		t.Fatal(err)
	}
	dst, err := database.OpenPostgresSetupApplication(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	now := time.Now().UTC()
	seed := database.BootstrapSeed{ID: "00000000-0000-4000-8000-000000000001", Organization: "ShiftBox", Origin: "https://management.test", IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test", Release: "development", MinGuestRelease: "development", Now: now}
	src, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "bootstrap.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// Stage only the selected setup state, including a real browser certificate
	// that the authority switch can verify before retiring the bootstrap store.
	for _, statement := range []string{
		`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES ('owner','owner@example.test','administrator','active','local','local_setup','time','time',2)`,
		`INSERT INTO bootstrap_administrator(installation_id,user_id,password_hash,credential_ready,delivered_at,created_at)
		SELECT id,'owner','old-temporary-hash',1,'time','time' FROM installations`,
		`UPDATE installations SET state='configuring'`,
		`INSERT INTO protected_values VALUES ('unrelated','not setup state')`,
	} {
		if _, err := src.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	session, token, err := auth.NewSession("source-session", "owner", "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, src, func(tx *sql.Tx) error { return auth.NewStore(src).SaveSession(ctx, tx, session) }); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	browserRoots := configureActivationCertificate(t, src, cipher, seed, now)
	const localOrigin = "https://127.0.0.1:8443"
	clock := func() time.Time { return now }
	setup := app.SetupHTTP{DB: src, InstallationID: id, Cipher: cipher, CertificateRoots: browserRoots, Origin: localOrigin,
		Admin: identity.PasswordAdmin{}, Login: &app.SetupLogin{},
		Postgres: &app.PostgresProfileSetup{DexURI: dexURI, CAFile: os.Getenv("SHIFTBOX_TEST_SETUP_CA"), Roots: roots},
		Journal:  operation.NewJournal(src, clock, uuid.NewString), Now: clock, NewID: uuid.NewString}
	var transfers int
	transferReady := false
	setup = setup.WithPostgresTransfer(func(_ context.Context, storage identity.DexPostgres, email, user string) error {
		transfers++
		if email != "owner@example.test" || user != "owner" || storage.Password == "" {
			return fmt.Errorf("wrong identity transfer scope")
		}
		if !transferReady {
			return errors.New("fixture transfer interruption")
		}
		return nil
	})
	handler, err := app.NewSetupHandler(setup)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, key, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, localOrigin+"/api/v1/setup/"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:55555"
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Origin", localOrigin)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), appURI) || strings.Contains(w.Body.String(), dexURI) {
			t.Fatalf("%s returned %d, want %d with redacted body", path, w.Code, want)
		}
	}
	request(http.MethodPut, "data-profile", "fixture-postgres-profile", fmt.Sprintf(`{"profile":"postgres","tlsMode":"verify-full","connectionUri":%q}`, appURI), 202)
	if err := database.Migrate(ctx, dst, "postgres"); err != nil {
		t.Fatal(err)
	}
	var sourceCiphertext string
	if err := src.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", activationCertificateID).Scan(&sourceCiphertext); err != nil {
		t.Fatal(err)
	}
	credentials := app.PostgresSetupCredentials{ApplicationURI: appURI, DexURI: dexURI, CAFile: os.Getenv("SHIFTBOX_TEST_SETUP_CA")}
	invalid := credentials
	invalid.DexURI = invalid.ApplicationURI
	if _, err := app.StagePostgresSetup(ctx, src, dst, id, invalid, cipher, now); err == nil {
		t.Fatal("staging accepted a shared PostgreSQL role")
	}
	var installations int
	if err := dst.QueryRowContext(ctx, "SELECT COUNT(*) FROM installations").Scan(&installations); err != nil || installations != 0 {
		t.Fatalf("failed staging left %d installations, error %v", installations, err)
	}
	for range 2 {
		owner, err := app.StagePostgresSetup(ctx, src, dst, id, credentials, cipher, now)
		if err != nil {
			t.Fatal(err)
		}
		if owner.ID != "owner" || owner.PrimaryEmail != "owner@example.test" {
			t.Fatal("staging changed owner identity")
		}
	}
	var copiedID, state, profile, settings string
	if err := dst.QueryRowContext(ctx, `SELECT i.id,i.state,i.data_profile,c.settings_json
	FROM installations i JOIN activated_configurations c ON i.id=c.installation_id AND c.retired_at IS NULL`).Scan(&copiedID, &state, &profile, &settings); err != nil {
		t.Fatal(err)
	}
	if copiedID != id || state != "configuring" || profile != "postgres" || !strings.Contains(settings, `"dataProfile":"postgres"`) {
		t.Fatal("incorrect staged configuration")
	}
	var ciphertext string
	if err := dst.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", activationCertificateID).Scan(&ciphertext); err != nil || ciphertext != sourceCiphertext {
		t.Fatal("TLS ciphertext changed")
	}
	for _, table := range []string{"sessions", "bootstrap_administrator"} {
		var count int
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("staging copied %s", table)
		}
	}
	var count int
	if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM protected_values").Scan(&count); err != nil || count != 2 {
		t.Fatal("staging did not retain exactly the certificate and identity settings")
	}
	if err := dst.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", id+":dex-postgres").Scan(&ciphertext); err != nil ||
		!strings.Contains(ciphertext, `"ciphertext":`) || strings.Contains(ciphertext, dexURI) {
		t.Fatalf("staged PostgreSQL identity credentials were not encrypted, error %v", err)
	}
	if _, err := dst.ExecContext(ctx, "UPDATE users SET primary_email='unrelated@example.test'"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StagePostgresSetup(ctx, src, dst, id, credentials, cipher, now); err == nil {
		t.Fatal("staging overwrote conflicting destination")
	}
	if _, err := dst.ExecContext(ctx, "UPDATE users SET primary_email='owner@example.test'"); err != nil {
		t.Fatal(err)
	}
	const key = "postgres-activation-fixture"
	if _, err := setup.ActivatePostgres(ctx, dst, "owner", key); err == nil {
		t.Fatal("activation accepted target before artifact and capacity validation")
	}
	if err := src.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&state); err != nil || state != "configuring" {
		t.Fatalf("readiness failure changed source state to %s, error %v", state, err)
	}
	if _, err := dst.ExecContext(ctx, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,is_default)
		VALUES('fixture','fixture','amd64','test','development','1','[]','{}','{}','{}',repeat('a',64),'fixture.utm','owner','now','available',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.ExecContext(ctx, `INSERT INTO capacity_budgets(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
		VALUES($1,4,4096,10240,'owner','now',1)`, id); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "activate", key, "", 500)
	if err := src.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&state); err != nil || state != "configuring" {
		t.Fatalf("transfer failure changed source state to %s, error %v", state, err)
	}
	transferReady = true
	request(http.MethodPost, "activate", key, "", 202)
	if transfers != 2 {
		t.Fatalf("activation attempted %d identity transfers, want one attempt and one retry", transfers)
	}
	request(http.MethodPost, "activate", key, "", 401)
	first, err := setup.ResumeRetiredPostgresSetup(ctx)
	if err != nil || first.Status != "succeeded" {
		t.Fatalf("activation = %s, %v, want succeeded", first.Status, err)
	}
	again, err := setup.ActivatePostgres(ctx, dst, "owner", key)
	if err != nil || again.ID != first.ID {
		t.Fatalf("activation replay = %s, %v, want %s", again.ID, err, first.ID)
	}
	if err := dst.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&state); err != nil || state != "active" {
		t.Fatalf("target state = %s, %v, want active", state, err)
	}
	runtimeURI, err := url.Parse(appURI)
	if err != nil {
		t.Fatal("invalid fixture connection URI")
	}
	query := runtimeURI.Query()
	query.Set("sslrootcert", credentials.CAFile)
	runtimeURI.RawQuery = query.Encode()
	runtimeDB, active, err := app.OpenActiveDatabase(ctx, config.Config{InstallationDir: t.TempDir(), DataProfile: "postgres",
		PostgresURI: runtimeURI.String(), ManagementOrigin: seed.Origin, ExternalAuthIntervalSeconds: 14400})
	if err != nil {
		t.Fatal(err)
	}
	runtimeDB.Close()
	if active.InstallationID != id || active.Settings.DataProfile != "postgres" {
		t.Fatal("runtime selected a different installation or data profile")
	}
	var revoked string
	if err := src.QueryRowContext(ctx, `SELECT i.state,s.revocation_reason FROM installations i CROSS JOIN sessions s
		WHERE s.id=$1 AND s.revoked_at IS NOT NULL`, session.ID).Scan(&state, &revoked); err != nil || state != "blocked" || revoked != "setup_activated" {
		t.Fatalf("source state = %s / %s, %v, want blocked and revoked", state, revoked, err)
	}
	recovered, err := setup.ResumePostgresActivation(ctx, dst)
	if err != nil || recovered.ID != first.ID {
		t.Fatalf("recovery selected %s, %v, want %s", recovered.ID, err, first.ID)
	}
}
