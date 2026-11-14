package config_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
	_ "modernc.org/sqlite"
)

// TestActivationGenerationIsAtomic prevents stale or failed activation from replacing the last durable configuration.
func TestActivationGenerationIsAtomic(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) { checkActivationGeneration(t, profile) })
	}
}

func checkActivationGeneration(t *testing.T, profile string) {
	t.Helper()
	ctx := context.Background()
	cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "activation.db"), Schema: "shiftbox_test"}
	if profile == "postgres" {
		cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if cfg.PostgresURI == "" {
			t.Skip("PostgreSQL fixture not configured")
		}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ddl, err := migrations.Files.ReadFile(profile + "/00001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Temporary tables exercise the store without changing existing fixture history.
	if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(ddl), "CREATE TABLE ", "CREATE TEMPORARY TABLE ")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES('00000000-0000-4000-8000-000000000001','configuring','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{workspace}-{service}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	activation := config.Activation{InstallationID: "00000000-0000-4000-8000-000000000001", ExpectedGeneration: 0, SettingsJSON: `{"profile":"sqlite"}`, ActorID: "setup", ActivatedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
	apply := func(tx *sql.Tx) error {
		generation, err := config.RecordActivation(ctx, tx, activation)
		if err == nil && generation != activation.ExpectedGeneration+1 {
			t.Errorf("RecordActivation generation = %d, want %d", generation, activation.ExpectedGeneration+1)
		}
		return err
	}
	if err := database.Transaction(ctx, db, apply); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, apply); !errors.Is(err, config.ErrActivationConflict) {
		t.Fatalf("RecordActivation(stale generation) = %v, want conflict", err)
	}
	activation.ExpectedGeneration = 1
	failure := errors.New("certificate activation failed")
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := apply(tx); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("activation rollback = %v, want injected error", err)
	}
	check := func(want int64) {
		t.Helper()
		var generation, active, count int64
		if err := db.QueryRowContext(ctx, `SELECT config_generation FROM installations WHERE id='00000000-0000-4000-8000-000000000001'`).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT generation FROM activated_configurations WHERE retired_at IS NULL`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM activated_configurations`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if generation != want || active != want || count != want {
			t.Errorf("activation generation/active/history = %d/%d/%d, want %d", generation, active, count, want)
		}
	}
	check(1)
	if err := database.Transaction(ctx, db, apply); err != nil {
		t.Fatal(err)
	}
	check(2)
	// Updating installation settings must preserve the active profile and use the
	// same transaction for the live row and its immutable generation snapshot.
	settings := config.InstallationSettings{OrganizationName: "Updated", DataProfile: "sqlite", ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400}
	activation.ExpectedGeneration = 2
	applySettings := func(tx *sql.Tx) error {
		_, err := config.RecordInstallationSettings(ctx, tx, activation, settings)
		return err
	}
	settings.DataProfile = "postgres"
	if err := database.Transaction(ctx, db, applySettings); !errors.Is(err, config.ErrProfileImmutable) {
		t.Fatalf("RecordInstallationSettings(profile switch) = %v, want immutable profile", err)
	}
	settings.DataProfile = "sqlite"
	settings.PreviewHostPattern = "{workspace}.{service}.preview.test"
	if err := database.Transaction(ctx, db, applySettings); err == nil {
		t.Fatal("RecordInstallationSettings(invalid hostname) succeeded")
	}
	settings.PreviewHostPattern = "{service}--{workspace}.preview.test"
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := applySettings(tx); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("RecordInstallationSettings(rollback) = %v, want injected error", err)
	}
	check(2)
	var name string
	if err := db.QueryRowContext(ctx, `SELECT organization_name FROM installations`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "ShiftBox" {
		t.Errorf("organization after rollback = %q, want ShiftBox", name)
	}
	if err := database.Transaction(ctx, db, applySettings); err != nil {
		t.Fatal(err)
	}
	check(3)
	var snapshot string
	if err := db.QueryRowContext(ctx, `SELECT settings_json FROM activated_configurations WHERE retired_at IS NULL`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var recorded config.InstallationSettings
	if err := json.Unmarshal([]byte(snapshot), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != settings {
		t.Errorf("recorded settings = %+v, want %+v", recorded, settings)
	}
	if err := db.QueryRowContext(ctx, `SELECT organization_name FROM installations`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != settings.OrganizationName {
		t.Errorf("organization after activation = %q, want %q", name, settings.OrganizationName)
	}
}
