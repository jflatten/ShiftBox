package database

import (
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TestPlatformArchitectureMigration verifies an upgraded catalog keeps its AMD64
// artifacts and references while admitting ARM64 and rejecting unknown architectures.
func TestPlatformArchitectureMigration(t *testing.T) {
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
			defer db.Close()
			migrator, err := migrationProvider(db, profile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := migrator.UpTo(ctx, 27); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
				VALUES('platform-test','platform@example.test','administrator','active','local','setup','now','now',1)`)
			const insertArtifact = `INSERT INTO platform_artifacts
				(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,
				provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,is_default)
				VALUES($1,$1,$2,'24.04','development','1','[]','{}','{}','{}',
				'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','platform-test','now','available',$3)`
			exec(insertArtifact, "amd-reference", "amd64", 1)
			exec("CREATE TABLE platform_test_reference (artifact_id TEXT NOT NULL REFERENCES platform_artifacts(id))")
			defer func() {
				for _, query := range []string{
					"DROP TABLE platform_test_reference",
					"DELETE FROM platform_artifacts WHERE id IN ('amd-reference','arm-reference','unknown-reference')",
					"DELETE FROM users WHERE id='platform-test'",
				} {
					if _, err := db.ExecContext(ctx, query); err != nil {
						t.Errorf("remove migration fixture: %v", err)
					}
				}
			}()
			exec("INSERT INTO platform_test_reference VALUES ('amd-reference')")
			if err := Migrate(ctx, db, profile); err != nil {
				t.Fatal(err)
			}
			exec(insertArtifact, "arm-reference", "arm64", 0)
			var got string
			err = db.QueryRowContext(ctx, `SELECT architecture FROM platform_test_reference r
				JOIN platform_artifacts a ON a.id=r.artifact_id WHERE a.is_default=1`).Scan(&got)
			if err != nil || got != "amd64" {
				t.Fatalf("retained default artifact = %q, %v, want amd64 and no error", got, err)
			}
			if _, err := db.ExecContext(ctx, insertArtifact, "unknown-reference", "s390x", 0); err == nil {
				t.Error("platform_artifacts accepted s390x, want a check constraint failure")
			}
			if _, err := db.ExecContext(ctx, "DELETE FROM platform_artifacts WHERE id='amd-reference'"); err == nil {
				t.Error("platform_artifacts deleted a referenced artifact, want a foreign key failure")
			}
			if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET architecture='amd64' WHERE id='arm-reference'"); err == nil {
				t.Error("platform_artifacts changed an available artifact, want an immutability failure")
			}
		})
	}
}
