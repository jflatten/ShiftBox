package integration_test

import (
	"context"
	"crypto/x509"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
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
	if err := database.Migrate(ctx, dst, "postgres"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seed := database.BootstrapSeed{ID: "00000000-0000-4000-8000-000000000001", Organization: "ShiftBox", Origin: "https://management.test", IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test", Release: "development", MinGuestRelease: "development", Now: now}
	src, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "bootstrap.db"), seed)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// Placeholder ciphertext is sufficient to verify byte-for-byte transfer.
	for _, statement := range []string{
		`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES ('owner','owner@example.test','administrator','active','local','local_setup','time','time',2)`,
		`INSERT INTO bootstrap_administrator(installation_id,user_id,password_hash,credential_ready,delivered_at,created_at)
		SELECT id,'owner','old-temporary-hash',1,'time','time' FROM installations`,
		`UPDATE installations SET state='configuring',config_generation=1`,
		`INSERT INTO protected_values VALUES ('tls','fixture ciphertext'),('unrelated','not setup state')`,
		`INSERT INTO browser_certificates VALUES ('tls','fixture chain','tls','time','time','fingerprint','active')`,
		`INSERT INTO activated_configurations SELECT id,1,
		'{"browserCertificateId":"tls","dataProfile":"sqlite"}','owner','time',NULL FROM installations`,
	} {
		if _, err := src.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	session, _, err := auth.NewSession("source-session", "owner", "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, src, func(tx *sql.Tx) error { return auth.NewStore(src).SaveSession(ctx, tx, session) }); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		owner, err := app.StagePostgresSetup(ctx, src, dst, id, now)
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
	if err := dst.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id='tls'").Scan(&ciphertext); err != nil || ciphertext != "fixture ciphertext" {
		t.Fatal("TLS ciphertext changed")
	}
	for _, table := range []string{"sessions", "bootstrap_administrator"} {
		var count int
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("staging copied %s", table)
		}
	}
	var count int
	if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM protected_values").Scan(&count); err != nil || count != 1 {
		t.Fatal("staging copied unrelated protected data")
	}
	if _, err := dst.ExecContext(ctx, "UPDATE users SET primary_email='unrelated@example.test'"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.StagePostgresSetup(ctx, src, dst, id, now); err == nil {
		t.Fatal("staging overwrote conflicting destination")
	}
}
