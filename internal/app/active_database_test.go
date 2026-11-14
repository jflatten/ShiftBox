package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestOpenActiveDatabaseRequiresActivation checks that runtime startup
// rejects missing or staged databases and verifies the committed settings.
func TestOpenActiveDatabaseRequiresActivation(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	cfg := config.Config{InstallationDir: dir, DataProfile: "sqlite", SQLitePath: path,
		ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	if _, _, err := OpenActiveDatabase(ctx, cfg); !errors.Is(err, ErrActiveDatabase) {
		t.Fatalf("open missing database = %v, want activation error", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created missing database: %v", err)
	}
	const id = "00000000-0000-4000-8000-000000000001"
	db, _, err := database.OpenBootstrap(ctx, path, database.BootstrapSeed{
		ID: id, Organization: "ShiftBox", Origin: cfg.ManagementOrigin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := OpenActiveDatabase(ctx, cfg); !errors.Is(err, ErrActiveDatabase) {
		t.Fatalf("open bootstrap state = %v, want activation error", err)
	}
	const settings = `{"browserCertificateId":"browser","organizationName":"ShiftBox","dataProfile":"sqlite","managementOrigin":"https://management.test","ideHostPattern":"{workspace}.ide.test","previewHostPattern":"{service}--{workspace}.preview.test","externalAuthIntervalSeconds":14400}`
	if _, err := db.ExecContext(ctx, `INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at)
		VALUES($1,1,$2,'owner','2026-11-14T00:00:00Z')`, id, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active',config_generation=1 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	opened, active, err := OpenActiveDatabase(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	opened.Close()
	if active.InstallationID != id || active.Generation != 1 || active.Settings.BrowserCertificateID != "browser" {
		t.Errorf("activation = %+v, want committed installation snapshot", active)
	}
	if active.ActiveRelease != "test" || active.MinimumGuestRelease != "test" {
		t.Errorf("activation releases = %q/%q, want both from the active database", active.ActiveRelease, active.MinimumGuestRelease)
	}
	// Startup must reject a schema with gaps even if the highest version
	// still matches the binary's migration set.
	if _, err := db.Exec("DELETE FROM goose_db_version WHERE version_id=2"); err != nil {
		t.Fatal(err)
	}
	if stale, _, err := OpenActiveDatabase(ctx, cfg); !errors.Is(err, ErrActiveDatabase) {
		if stale != nil {
			stale.Close()
		}
		t.Errorf("open active database with a schema gap = %v, want activation error", err)
	}
	if _, err := db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(2,1)"); err != nil {
		t.Fatal(err)
	}
	cfg.ManagementOrigin = "https://other.test"
	if _, _, err := OpenActiveDatabase(ctx, cfg); !errors.Is(err, ErrActiveDatabase) {
		t.Errorf("open with changed origin = %v, want activation error", err)
	}
	cfg.ManagementOrigin = "https://management.test"
	if _, err := db.Exec("ALTER TABLE operations DROP COLUMN originating_action"); err != nil {
		t.Fatal(err)
	}
	stale, _, err := OpenActiveDatabase(ctx, cfg)
	if stale != nil {
		stale.Close()
	}
	if !errors.Is(err, ErrActiveDatabase) || !errors.Is(err, database.ErrSchemaDrift) {
		t.Errorf("open active database with foundation drift = %v, want schema drift", err)
	}
}

// TestOpenActiveDatabaseDoesNotFallBack verifies that failure of the selected
// PostgreSQL target never opens or mutates the bootstrap SQLite authority.
func TestOpenActiveDatabaseDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{InstallationDir: dir, DataProfile: "postgres",
		PostgresURI:      "postgres://fixture:private@127.0.0.1:1/shiftbox?sslmode=verify-full&connect_timeout=1",
		ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	sqlitePath := filepath.Join(dir, "bootstrap.db")
	if err := os.WriteFile(sqlitePath, []byte("bootstrap fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenActiveDatabase(t.Context(), cfg); !errors.Is(err, ErrActiveDatabase) {
		t.Fatalf("open unavailable PostgreSQL = %v, want activation error", err)
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 1 || files[0].Name() != "bootstrap.db" {
		t.Errorf("startup touched bootstrap storage: %v, %v", files, err)
	}
	if content, err := os.ReadFile(sqlitePath); err != nil || string(content) != "bootstrap fixture" {
		t.Errorf("bootstrap fixture changed: %v", err)
	}
}
