package backup

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/database"
)

// TestRestorePostgresSchemas loads native dumps with replacement roles and
// proves that retries refuse an occupied target.
func TestRestorePostgresSchemas(t *testing.T) {
	sourceApp, sourceDex := os.Getenv("SHIFTBOX_TEST_BACKUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_BACKUP_DEX_URI")
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_RESTORE_APP_URI"), os.Getenv("SHIFTBOX_TEST_RESTORE_DEX_URI")
	caPath := os.Getenv("SHIFTBOX_TEST_RESTORE_CA")
	if sourceApp == "" || sourceDex == "" || appURI == "" || dexURI == "" || caPath == "" {
		t.Skip("requires isolated source and replacement PostgreSQL databases")
	}
	openFixtureStore(t, sourceApp, "shiftbox")
	openFixtureStore(t, sourceDex, "dex")
	ctx := t.Context()
	stage := checkpointStage(t)
	_, err := StagePostgresCheckpoint(ctx, stage, sourceApp, sourceDex, checkpointInputs(t, t.TempDir()),
		func(_ context.Context) (func(), error) { return func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal("cannot read fixture CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("cannot load fixture CA")
	}
	target := database.PostgresSetupTarget{ApplicationURI: appURI, DexURI: dexURI, Roots: roots}
	// A damaged second archive must be rejected before loading the first.
	path := filepath.Join(stage, "application.dump")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid dump"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); err == nil {
		t.Fatal("accepted damaged native archive")
	}
	if err := ValidatePostgresReplacement(ctx, target); err != nil {
		t.Fatal("damaged dump changed the empty replacement target")
	}
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); !errors.Is(err, ErrRestoreTarget) {
		t.Fatalf("second load into occupied target = %v; want rejection", err)
	}
	for _, store := range []struct{ uri, schema string }{{appURI, "shiftbox"}, {dexURI, "dex"}} {
		u, err := url.Parse(store.uri)
		if err != nil {
			t.Fatal("invalid fixture URI")
		}
		query := u.Query()
		query.Set("sslrootcert", caPath)
		u.RawQuery = query.Encode()
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			t.Fatal("cannot open replacement schema")
		}
		t.Cleanup(func() { db.Close() })
		var row string
		if err := db.QueryRowContext(ctx, "SELECT note FROM "+store.schema+".backup_probe").Scan(&row); err != nil || row != "before_cutoff" {
			t.Fatalf("restored %s data = %q, %v; want checkpoint data", store.schema, row, err)
		}
	}
}
