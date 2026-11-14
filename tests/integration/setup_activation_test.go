package integration_test

import (
	"context"
	"crypto/x509"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
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
