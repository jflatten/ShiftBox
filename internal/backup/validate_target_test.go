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

// TestValidateSQLiteReplacement checks that validation never creates, merges
// with, or follows an existing replacement path.
func TestValidateSQLiteReplacement(t *testing.T) {
	dir := checkpointStage(t)
	appPath, dexPath := filepath.Join(dir, "app.db"), filepath.Join(dir, "dex.db")
	if err := ValidateSQLiteReplacement(dir, appPath, dexPath); err != nil {
		t.Fatalf("empty replacement rejected: %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("validation modified the replacement directory: %v", err)
	}
	if err := os.WriteFile(appPath, []byte("existing application"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteReplacement(dir, appPath, dexPath); err == nil {
		t.Fatal("validation accepted an existing application")
	}
	if err := os.Remove(appPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "absent"), dexPath); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteReplacement(dir, appPath, dexPath); err == nil {
		t.Fatal("validation accepted a dangling identity-store symlink")
	}
	if err := ValidateSQLiteReplacement(dir, appPath, filepath.Join(t.TempDir(), "dex.db")); err == nil {
		t.Fatal("validation accepted a Dex store outside its protected destination")
	}
}

// TestValidatePostgresReplacement requires separate owners and refuses a
// populated schema without changing it.
func TestValidatePostgresReplacement(t *testing.T) {
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_RESTORE_APP_URI"), os.Getenv("SHIFTBOX_TEST_RESTORE_DEX_URI")
	if appURI == "" || dexURI == "" {
		t.Skip("requires empty scoped PostgreSQL restore schemas")
	}
	caPath := os.Getenv("SHIFTBOX_TEST_RESTORE_CA")
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal("could not read fixture CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("could not parse fixture CA")
	}
	ctx := t.Context()
	target := database.PostgresSetupTarget{ApplicationURI: appURI, DexURI: dexURI, Roots: roots}
	if err := ValidatePostgresReplacement(ctx, target); err != nil {
		t.Fatalf("empty scoped target rejected: %v", err)
	}
	wrongOwner := target
	wrongOwner.DexURI = appURI
	if err := ValidatePostgresReplacement(ctx, wrongOwner); !errors.Is(err, ErrRestoreTarget) {
		t.Fatalf("replacement with shared owner = %v; want target rejection", err)
	}
	dsn, err := url.Parse(appURI)
	if err != nil {
		t.Fatal("could not parse fixture URI")
	}
	q := dsn.Query()
	q.Set("sslrootcert", caPath)
	dsn.RawQuery = q.Encode()
	db, err := sql.Open("pgx", dsn.String())
	if err != nil {
		t.Fatal("could not open fixture database")
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE shiftbox.restore_probe (val int)"); err != nil {
		t.Fatal("could not populate fixture schema")
	}
	defer db.ExecContext(context.WithoutCancel(ctx), "DROP TABLE shiftbox.restore_probe")
	if _, err := db.ExecContext(ctx, "INSERT INTO shiftbox.restore_probe VALUES (42)"); err != nil {
		t.Fatal("could not seed fixture schema")
	}
	if err := ValidatePostgresReplacement(ctx, target); !errors.Is(err, ErrRestoreTarget) {
		t.Fatalf("populated replacement = %v; want target rejection", err)
	}
	var kept int
	if err := db.QueryRowContext(ctx, "SELECT val FROM shiftbox.restore_probe").Scan(&kept); err != nil || kept != 42 {
		t.Fatalf("replacement row = %d, %v; want validation to preserve it", kept, err)
	}
}
