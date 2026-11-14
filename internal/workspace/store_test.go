package workspace

import (
	"context"
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
	id := "00000000-0000-4000-8000-000000000023"
	const stamp = "2026-11-14T00:00:00Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO installations
		(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','ShiftBox',$3,'https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','test','test',$2,$2,1)`, id, stamp, profile); err != nil {
		t.Fatal(err)
	}
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
	now, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCapacityBudget(ctx, id, budget, 0, "admin", now); err != nil {
		t.Fatal(err)
	}
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
