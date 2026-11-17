package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	_ "modernc.org/sqlite"
)

// TestCapacityConfiguration persists a validated profile and budget and
// rejects stale writes and profiles outside the provider's advertised limits.
func TestCapacityConfiguration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			checkCapacityConfiguration(t, profile)
		})
	}
}

func checkCapacityConfiguration(t *testing.T, profile string) {
	t.Helper()
	ctx := context.Background()
	db := openWorkspaceDatabase(t, profile)
	id := testInstallation
	store := NewStore(db)
	caps := provider.Capabilities{
		Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 10 << 20},
		Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 << 20, DiskBytes: 100 << 20},
	}
	intended := ResourceProfile{ID: "small", Name: "Small", Allocation: Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, Enabled: true}
	if err := store.CreateResourceProfile(ctx, intended, caps); err != nil {
		t.Fatal(err)
	}
	got, err := store.ResourceProfile(ctx, intended.ID)
	intended.Version = 1
	if err != nil || got != intended {
		t.Fatalf("stored profile = %+v, %v, want %+v", got, err, intended)
	}
	duplicate := intended
	duplicate.ID = "another"
	if err := store.CreateResourceProfile(ctx, duplicate, caps); !errors.Is(err, ErrCapacityConflict) {
		t.Errorf("duplicate profile name = %v, want conflict", err)
	}
	intended.CPU = 16
	if err := store.CreateResourceProfile(ctx, intended, caps); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Errorf("oversized profile = %v, want unsupported", err)
	}
	budget := Allocation{CPU: 8, MemoryMiB: 8192, DiskMiB: 300}
	now, err := time.Parse(time.RFC3339, testStamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCapacityBudget(ctx, id, budget, 0, "admin", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM capacity_budgets WHERE installation_id=$1", id); err != nil {
			t.Errorf("remove capacity fixture: %v", err)
		}
		if _, err := db.Exec("DELETE FROM resource_profiles WHERE id=$1", intended.ID); err != nil {
			t.Errorf("remove resource profile fixture: %v", err)
		}
	})
	budget.CPU--
	if err := store.SetCapacityBudget(ctx, id, budget, 1, "admin", now); err != nil {
		t.Fatal(err)
	}
	budget.CPU--
	if err := store.SetCapacityBudget(ctx, id, budget, 1, "admin", now); !errors.Is(err, ErrCapacityConflict) {
		t.Errorf("stale budget update = %v, want conflict", err)
	}
	persisted, version, err := store.CapacityBudget(ctx, id)
	if err != nil || version != 2 || persisted.CPU != 7 {
		t.Errorf("stored budget = %+v, version %d, %v, want 7 CPU and version 2", persisted, version, err)
	}
}

const (
	testInstallation = "00000000-0000-4000-8000-000000000023"
	testStamp        = "2026-11-14T00:00:00Z"
)

// TestUnlimitedCapacityConfiguration persists independent unlimited limits and
// rejects negative writes without changing the saved budget or its revision.
func TestUnlimitedCapacityConfiguration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			db := openWorkspaceDatabase(t, profile)
			store := NewStore(db)
			now := time.Unix(10000, 0).UTC()
			t.Cleanup(func() {
				if _, err := db.Exec("DELETE FROM capacity_budgets WHERE installation_id=$1", testInstallation); err != nil {
					t.Errorf("remove unlimited budget fixture: %v", err)
				}
			})
			for version, limit := range []Allocation{{}, {CPU: 4}, {MemoryMiB: 4096}, {DiskMiB: 100}} {
				if err := store.SetCapacityBudget(t.Context(), testInstallation, limit, int64(version), "admin", now); err != nil {
					t.Fatalf("SetCapacityBudget(%+v, version %d) = %v, want saved", limit, version, err)
				}
				got, revision, err := store.CapacityBudget(t.Context(), testInstallation)
				if err != nil || got != limit || revision != int64(version+1) {
					t.Errorf("CapacityBudget(%q) = %+v, version %d, %v, want %+v, version %d", testInstallation, got, revision, err, limit, version+1)
				}
			}
			for _, limit := range []Allocation{{CPU: -1}, {MemoryMiB: -1}, {DiskMiB: -1}} {
				if err := store.SetCapacityBudget(t.Context(), testInstallation, limit, 4, "admin", now); err == nil {
					t.Errorf("SetCapacityBudget(%+v, version 4) = nil, want invalid budget error", limit)
				}
			}
			got, revision, err := store.CapacityBudget(t.Context(), testInstallation)
			if err != nil || got != (Allocation{DiskMiB: 100}) || revision != 4 {
				t.Errorf("CapacityBudget after negative writes = %+v, version %d, %v, want disk 100, version 4", got, revision, err)
			}
		})
	}
}

func openWorkspaceDatabase(t *testing.T, profile string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "workspace.db")}
	if profile == "postgres" {
		cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_CAPACITY_POSTGRES_URI")
		if cfg.PostgresURI == "" {
			t.Skip("requires a disposable PostgreSQL database")
		}
		cfg.Schema = "shiftbox_test"
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO installations
		(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','ShiftBox',$3,'https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','test','test',$2,$2,1) ON CONFLICT(id) DO NOTHING`, testInstallation, testStamp, profile); err != nil {
		t.Fatal(err)
	}
	return db
}
