package database

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstanceTypeMigration keeps profiles created on schema 30 when the
// instance type column is added and accepts new pinned values on schema 31.
func TestInstanceTypeMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := t.Context()
			uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
			if profile == "postgres" && uri == "" {
				t.Skip("requires the disposable PostgreSQL fixture")
			}
			db, err := Open(ctx, Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "fixture.db"),
				PostgresURI: uri, Schema: "shiftbox_test"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			migrator, err := migrationProvider(db, profile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := migrator.UpTo(ctx, 30); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
				VALUES('migration-fixture','Legacy EC2 profile',2,4096,32768,1,1)`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec("DELETE FROM resource_profiles WHERE id='migration-fixture'"); err != nil {
					t.Errorf("remove migration fixture: %v", err)
				}
			})
			if err := Migrate(ctx, db, profile); err != nil {
				t.Fatal(err)
			}
			if err := CheckSchema(ctx, db, profile); err != nil {
				t.Fatal(err)
			}
			var disk int64
			var kind string
			err = db.QueryRowContext(ctx, "SELECT disk_mib,instance_type FROM resource_profiles WHERE id='migration-fixture'").Scan(&disk, &kind)
			if err != nil || disk != 32768 || kind != "" {
				t.Fatalf("migrated profile = %d/%q, %v; want 32768 MiB and legacy type marker", disk, kind, err)
			}
		})
	}
}
