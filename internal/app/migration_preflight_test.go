package app

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestMigrationKeyAccessible covers key loss and replacement while a running
// controller still has a usable copy in memory.
func TestMigrationKeyAccessible(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "installation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "installation.key")
	original := bytes.Repeat([]byte{1}, 32)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cipher, err := loadControllerCipher(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := &ControllerState{cipher: cipher}
	if !state.migrationKeyAccessible(dir) {
		t.Error("migrationKeyAccessible rejected the retained installation key")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{2}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if state.migrationKeyAccessible(dir) {
		t.Error("migrationKeyAccessible accepted a different key on disk")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if state.migrationKeyAccessible(dir) {
		t.Error("migrationKeyAccessible accepted a key without private permissions")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if state.migrationKeyAccessible(dir) {
		t.Error("migrationKeyAccessible accepted a missing key on disk")
	}
}

// TestMigrationDatabaseOwned checks that preflight rejects a missing identity
// login, the wrong identity role, and drift on the application connection.
func TestMigrationDatabaseOwned(t *testing.T) {
	ctx := t.Context()
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_SETUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI")
	ca := os.Getenv("SHIFTBOX_TEST_SETUP_CA")
	if appURI == "" || dexURI == "" || ca == "" {
		t.Skip("requires scoped PostgreSQL application and Dex test roles")
	}
	dex, err := postgresDexStorage(appURI, dexURI, ca)
	if err != nil {
		t.Fatal("invalid scoped test configuration")
	}
	db, err := openSelectedPostgres(ctx, PostgresSetupCredentials{ApplicationURI: appURI, DexURI: dexURI, CAFile: ca}, false)
	if err != nil {
		t.Fatalf("connect to scoped fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	state := &ControllerState{DB: db, Installation: ActiveInstallation{Settings: config.InstallationSettings{DataProfile: "postgres"}}}
	if state.migrationDatabaseOwned(ctx) {
		t.Error("migrationDatabaseOwned accepted a missing Dex role")
	}
	state.DexPostgres = &dex
	if !state.migrationDatabaseOwned(ctx) {
		t.Error("migrationDatabaseOwned rejected scoped application and Dex roles")
	}
	wrongLogin := dex
	wrongLogin.Password += "-invalid"
	state.DexPostgres = &wrongLogin
	if state.migrationDatabaseOwned(ctx) {
		t.Error("migrationDatabaseOwned accepted an unusable Dex login")
	}
	state.DexPostgres = &dex
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "SET search_path TO public")
		return err
	}); err != nil {
		t.Fatal("could not change the fixture search path")
	}
	if state.migrationDatabaseOwned(ctx) {
		t.Error("migrationDatabaseOwned accepted drift in the application search path")
	}
}
