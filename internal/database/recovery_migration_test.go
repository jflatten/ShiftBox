package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TestRecoveryMigration checks that each database requires backup completion
// evidence and review approval metadata before accepting the terminal states.
func TestRecoveryMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := t.Context()
			cfg := Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "fixture.db"),
				PostgresURI: os.Getenv("SHIFTBOX_TEST_POSTGRES_URI"), Schema: "shiftbox_test"}
			if profile == "postgres" && cfg.PostgresURI == "" {
				t.Skip("requires the disposable PostgreSQL fixture")
			}
			db, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := Migrate(ctx, db, profile); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := tx.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			reject := func(query string, args ...any) {
				t.Helper()
				exec("SAVEPOINT constraint_test")
				if _, err := tx.ExecContext(ctx, query, args...); err == nil {
					t.Errorf("schema accepted invalid state in %s", query)
				}
				exec("ROLLBACK TO SAVEPOINT constraint_test")
				exec("RELEASE SAVEPOINT constraint_test")
			}
			exec(`INSERT INTO operations(id,type,generation,requested_by,request_key,status,created_at,version)
				VALUES('backup-migration-fixture','backup',1,'operator','backup-migration-fixture','pending','now',1),
				('restore-migration-fixture','restore',1,'operator','restore-migration-fixture','pending','now',1)`)
			exec(`INSERT INTO backup_records(id,operation_id,format_version,product_release,schema_versions_json,state,created_by,created_at)
				VALUES('backup','backup-migration-fixture',1,'development','{}','preparing','operator','now')`)
			reject("UPDATE backup_records SET state='completed'")
			reject("UPDATE backup_records SET state='invented'")
			digest := strings.Repeat("a", 64)
			exec("UPDATE backup_records SET state='encrypting' WHERE id='backup'")
			reject("UPDATE backup_records SET state='completed', manifest_digest='bad', archive_digest=$1, size_bytes=100, completed_at='now' WHERE id='backup'", digest)
			exec("UPDATE backup_records SET state='completed',manifest_digest=$1,archive_digest=$1,size_bytes=100,completed_at='now' WHERE id='backup'", digest)
			reject("UPDATE backup_records SET archive_digest=NULL WHERE id='backup'")
			exec(`INSERT INTO recovery_reviews(id,restore_operation_id,state,started_by,started_at,version)
				VALUES('review','restore-migration-fixture','identity_review','operator','now',1)`)
			reject("UPDATE recovery_reviews SET state='activated' WHERE id='review'")
			exec("UPDATE recovery_reviews SET state='ready' WHERE id='review'")
			exec("UPDATE recovery_reviews SET state='activated',approved_by='operator',activated_at='now' WHERE id='review'")
			reject("UPDATE recovery_reviews SET approved_by=NULL WHERE id='review'")
			var name string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM recovery_reviews WHERE id='review'").Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name != "activated" {
				t.Errorf("review state = %q, want activated", name)
			}
		})
	}
}
