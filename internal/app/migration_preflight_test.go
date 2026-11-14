package app

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

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
