package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestUnlimitedCapacityMigration upgrades an existing finite budget without
// changing its values or revision, then permits zero but rejects negative limits.
func TestUnlimitedCapacityMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			cfg := Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "capacity.db")}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("requires the disposable PostgreSQL fixture")
				}
				admin, err := sql.Open("pgx", cfg.PostgresURI)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { admin.Close() })
				cfg.Schema = "shiftbox_test_capacity_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+cfg.Schema); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := admin.Exec("DROP SCHEMA " + cfg.Schema + " CASCADE"); err != nil {
						t.Errorf("remove capacity migration schema: %v", err)
					}
				})
			}
			db, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			migrator, err := migrationProvider(db, profile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := migrator.UpTo(t.Context(), 33); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO installations
				(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,
				active_release,min_guest_release,created_at,updated_at,version)
				VALUES('00000000-0000-4000-8000-000000000034','active','ShiftBox',$1,'https://management.test','{workspace}.ide.test',
				'{service}--{workspace}.preview.test','test','test','created','updated',1)`, profile); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO capacity_budgets
				(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
				VALUES('00000000-0000-4000-8000-000000000034',8,8192,100,'admin','stamp',7)`); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(t.Context(), db, profile); err != nil {
				t.Fatal(err)
			}
			var cpu, memory, disk, version int64
			var actor, stamp string
			if err := db.QueryRow(`SELECT cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version
				FROM capacity_budgets WHERE installation_id='00000000-0000-4000-8000-000000000034'`).Scan(&cpu, &memory, &disk, &actor, &stamp, &version); err != nil {
				t.Fatal(err)
			}
			if cpu != 8 || memory != 8192 || disk != 100 || actor != "admin" || stamp != "stamp" || version != 7 {
				t.Errorf("migrated budget = %d/%d/%d, %q/%q, version %d, want 8/8192/100, admin/stamp, version 7", cpu, memory, disk, actor, stamp, version)
			}
			for _, field := range []string{"cpu_limit", "memory_mib_limit", "disk_mib_limit"} {
				if _, err := db.Exec("UPDATE capacity_budgets SET " + field + "=0"); err != nil {
					t.Errorf("set unlimited %s = %v, want accepted", field, err)
				}
				if _, err := db.Exec("UPDATE capacity_budgets SET " + field + "=-1"); err == nil {
					t.Errorf("set negative %s = nil, want constraint error", field)
				}
			}
			if err := CheckSchema(t.Context(), db, profile); err != nil {
				t.Errorf("CheckSchema after unlimited capacity migration = %v, want valid", err)
			}
		})
	}
}
