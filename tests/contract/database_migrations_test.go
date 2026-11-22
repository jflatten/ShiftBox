package contract_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/launchlog"
	_ "modernc.org/sqlite"
)

// TestDatabaseTransactions ensures a failed management mutation leaves no partial write.
func TestDatabaseTransactions(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		// Run the same failure contract on both supported persistence profiles.
		t.Run(profile, func(t *testing.T) {
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "state.db")}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				cfg.Schema = "shiftbox_test"
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL parity requires SHIFTBOX_TEST_POSTGRES_URI and owned shiftbox_test schema")
				}
			}
			ctx := context.Background()
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(ctx, "CREATE TEMPORARY TABLE transaction_fixture (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected failure after first write")
			err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "INSERT INTO transaction_fixture VALUES (1)"); err != nil {
					return err
				}
				return failure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("Transaction(failure) = %v, want injected error", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transaction_fixture").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("failed transaction retained %d rows, want 0", count)
			}
		})
	}
}

// TestFoundationRejectsConflictingIntent ensures retries cannot create two durable operations.
func TestFoundationRejectsConflictingIntent(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO operations (id, type, generation, requested_by, request_key, status, created_at, version) VALUES (?, 'provision', 1, 'actor', 'same-request', ?, '2026-11-13T00:00:00Z', 1)`
	if _, err := db.ExecContext(ctx, insert, "one", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, insert, "two", "pending"); err == nil {
		t.Error("duplicate idempotency key accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO operations (id, type, generation, requested_by, request_key, status, created_at, version) VALUES ('three', 'provision', 1, 'actor', 'other', 'invented', '2026-11-13T00:00:00Z', 1)`); err == nil {
		t.Error("unknown operation state accepted")
	}
}

// TestMigrationProfiles advances both dialects to the same durable foundation version.
func TestMigrationProfiles(t *testing.T) {
	versions := make(map[string]int64)
	for _, profile := range []string{"sqlite", "postgres"} {
		// Replaying migrations after a restart must preserve the installed version.
		t.Run(profile, func(t *testing.T) {
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "state.db"), Schema: "shiftbox_test"}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL fixture not configured")
				}
			}
			ctx := context.Background()
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			for range 2 {
				if err := database.Migrate(ctx, db, profile); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.CheckSchema(ctx, db, profile); err != nil {
				t.Fatalf("CheckSchema(%s) after migration = %v, want compatible schema", profile, err)
			}
			// Both persistence profiles must install the scoped helper table.
			var helperCredentials int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(credential_digest) FROM helper_credentials").Scan(&helperCredentials); err != nil {
				t.Fatalf("helper credential migration (%s) = %v, want installed table", profile, err)
			}
			version, err := database.Version(ctx, db, profile)
			if err != nil || version < 1 {
				t.Fatalf("Version(%s) = %d, %v; want installed foundation", profile, version, err)
			}
			// Execute the diagnostics upserts on both dialects, rolling back the disposable launch.
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			const operationID = "migration-launch-timing"
			stamp := time.Now().UTC()
			if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
				VALUES($1,'provision',1,'actor','migration-launch-timing','pending',$2,1,'workspace.launch')`, operationID, stamp.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := launchlog.RecordCache(ctx, tx, operationID, launchlog.CacheSample{StartedAt: stamp,
					FinishedAt: stamp.Add(time.Second), CachedBytes: 10, Status: 200}); err != nil {
					t.Fatal(err)
				}
			}
			if err := launchlog.RecordProvider(ctx, tx, operationID, "vm_create", stamp, stamp.Add(time.Second), nil); err != nil {
				t.Fatal(err)
			}
			info, err := launchlog.Read(ctx, tx, operationID, stamp)
			if err != nil || info.Cache == nil || info.Cache.CachedBytes != 20 || info.Cache.WindowMS != 1000 || len(info.Events) != 1 {
				t.Fatalf("launch diagnostics (%s) = %+v, %v", profile, info, err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			versions[profile] = version
			if err := database.Rollback(ctx, db, profile); !errors.Is(err, database.ErrRecoveryRequired) {
				t.Errorf("Rollback(%s) = %v, want guarded recovery requirement", profile, err)
			}
		})
	}
	if postgres, ok := versions["postgres"]; ok && postgres != versions["sqlite"] {
		t.Errorf("schema versions differ: postgres=%d sqlite=%d", postgres, versions["sqlite"])
	}
}

// TestBuiltImageCacheMigration checks that both database profiles keep one
// visible image per project and platform and reject unbounded upload records.
func TestBuiltImageCacheMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		// Exercise the same policy and constraints in each supported dialect.
		t.Run(profile, func(t *testing.T) {
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "built-image-cache.db"), Schema: "shiftbox_test"}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL fixture not configured")
				}
			}
			ctx := t.Context()
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := database.Migrate(ctx, db, profile); err != nil {
				t.Fatalf("Migrate(%s) = %v, want success", profile, err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Rollback() })
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := tx.ExecContext(ctx, query, args...); err != nil {
					t.Fatalf("ExecContext(%s, %v) = %v, want success", query, args, err)
				}
			}
			reject := func(query string, args ...any) {
				t.Helper()
				// PostgreSQL requires rollback to a savepoint after a rejected statement.
				exec("SAVEPOINT built_image_constraint")
				_, err := tx.ExecContext(ctx, query, args...)
				exec("ROLLBACK TO SAVEPOINT built_image_constraint")
				exec("RELEASE SAVEPOINT built_image_constraint")
				if err == nil {
					t.Errorf("ExecContext(%s, %v) succeeded, want a constraint failure", query, args)
				}
			}

			const installation = "00000000-0000-4000-8000-000000000009"
			const stamp = "2026-11-20T17:00:00Z"
			exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
				preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
				VALUES($1,'active','Built cache fixture',$2,'https://management.test','{workspace}.ide.test',
				'{service}--{workspace}.preview.test','release','release',$3,$3,1)`, installation, profile, stamp)

			checkPolicy := func(enabled, version, generation int64, cutoff sql.NullInt64) {
				t.Helper()
				var gotEnabled, gotVersion, gotGeneration int64
				var gotCutoff sql.NullInt64
				err := tx.QueryRowContext(ctx, `SELECT built_image_enabled,built_image_version,
					built_image_generation,built_image_purge_cutoff FROM installations WHERE id=$1`, installation).
					Scan(&gotEnabled, &gotVersion, &gotGeneration, &gotCutoff)
				if err != nil || gotEnabled != enabled || gotVersion != version || gotGeneration != generation || gotCutoff != cutoff {
					t.Fatalf("built-image policy = (%d, %d, %d, %v), %v, want (%d, %d, %d, %v)",
						gotEnabled, gotVersion, gotGeneration, gotCutoff, err, enabled, version, generation, cutoff)
				}
			}
			checkPolicy(1, 1, 1, sql.NullInt64{})
			reject("UPDATE installations SET built_image_enabled=2 WHERE id=$1", installation)
			reject("UPDATE installations SET built_image_version=0 WHERE id=$1", installation)
			reject("UPDATE installations SET built_image_generation=0 WHERE id=$1", installation)
			exec(`UPDATE installations SET built_image_enabled=0,built_image_version=2,
				built_image_generation=2,built_image_purge_cutoff=1 WHERE id=$1`, installation)
			checkPolicy(0, 2, 2, sql.NullInt64{Int64: 1, Valid: true})
			exec(`UPDATE installations SET built_image_enabled=1,built_image_version=3,
				built_image_generation=3 WHERE id=$1`, installation)
			checkPolicy(1, 3, 3, sql.NullInt64{Int64: 1, Valid: true})

			configDigest := strings.Repeat("a", 64)
			manifestDigest := "sha256:" + strings.Repeat("b", 64)
			exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
				VALUES('cache-owner','cache-owner@example.test','administrator','active','local','setup',$1,$1,1)`, stamp)
			exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
				owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
				VALUES('cache-artifact','cache-release','amd64','test','release','1','[]','{}','{}','{}',$1,
				'fixture.utm','cache-owner',$2,'available')`, configDigest, stamp)
			exec(`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
				VALUES('cache-profile','Cache fixture',2,1024,40,1,1)`)
			for _, project := range []string{"cache-project", "other-cache-project"} {
				exec(`INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
					VALUES($1,'cache-owner',$1,'active',1,$2,$2,1)`, project, stamp)
				exec(`INSERT INTO template_revisions(template_id,revision,repository_url,devcontainer_path,resource_profile_id,
					platform_artifact_id,secret_names_json,created_by,created_at)
					VALUES($1,1,'https://github.com/example/repo','.devcontainer/devcontainer.json',
					'cache-profile','cache-artifact','[]','cache-owner',$2)`, project, stamp)
			}
			exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
				repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
				desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version)
				VALUES('cache-workspace',$1,'cache-owner','cache-project',1,'cache-artifact',
				'https://github.com/example/repo','.devcontainer/devcontainer.json',2,1024,40,'{}',
				'running','provisioning',1,'cache-fixture',$2,$2,1)`, installation, stamp)
			exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
				VALUES('cache-operation','cache-workspace','provision',1,'cache-owner','cache-fixture','running',$1,1,'workspace.launch')`, stamp)
			exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES('cache-operation',1,'running')`)

			const slot = `INSERT INTO built_image_slots(installation_id,project_id,target_os,target_arch,
				repository_url,config_path,config_sha256,manifest_digest,policy_generation,storage_generation,storage_path,storage_scope,
				publication_kind,origin_revision,promoted_at)
				VALUES($1,$2,'linux',$3,'https://github.com/example/repo','.devcontainer/devcontainer.json',
				$4,$5,3,2,'/var/cache/shiftbox',$6,'launch',$7,$8)`
			originRevision := strings.Repeat("c", 40)
			exec(slot, installation, "cache-project", "amd64", configDigest, manifestDigest, "cache-scope-amd64", originRevision, stamp)
			reject(slot, installation, "cache-project", "amd64", configDigest, "sha256:"+strings.Repeat("d", 64), "cache-scope-conflict", originRevision, stamp)
			exec(slot, installation, "cache-project", "arm64", configDigest, manifestDigest, "cache-scope-arm64", originRevision, stamp)
			exec(slot, installation, "other-cache-project", "amd64", configDigest, manifestDigest, "cache-scope-other", originRevision, stamp)

			const attempt = `INSERT INTO built_image_attempts(id,installation_id,project_id,workspace_id,workspace_generation,
				operation_id,dispatch_version,target_os,target_arch,repository_url,config_path,config_sha256,policy_generation,
				source_authorization_version,kind,state,created_at,expires_at,storage_path,storage_generation)
				VALUES($1,$2,'cache-project','cache-workspace',1,'cache-operation',$3,'linux','amd64',
				'https://github.com/example/repo','.devcontainer/devcontainer.json',$4,$5,2,'launch',$6,$7,$8,'/var/cache/shiftbox',3)`
			const expiry = "2026-11-20T17:05:00Z"
			exec(attempt, "cache-attempt", installation, 1, configDigest, 3, "admitted", stamp, expiry)
			reject(attempt, "bad-state", installation, 1, configDigest, 3, "published", stamp, expiry)
			reject(attempt, "no-expiry", installation, 1, configDigest, 3, "receiving", stamp, nil)
			reject(attempt, "invalid-expiry", installation, 1, configDigest, 3, "receiving", stamp, stamp)
			reject(attempt, "invalid-dispatch", installation, 0, configDigest, 3, "admitted", stamp, expiry)
			reject(attempt, "invalid-policy", installation, 1, configDigest, 0, "admitted", stamp, expiry)
			exec(`UPDATE built_image_attempts SET state='receiving' WHERE id='cache-attempt'`)
			exec(`UPDATE built_image_attempts SET state='complete',manifest_digest=$1 WHERE id='cache-attempt'`, manifestDigest)
			exec(`UPDATE built_image_attempts SET state='promoted' WHERE id='cache-attempt'`)
		})
	}
}

// TestPostgresRejectsUnownedSchema checks actual server ownership before any migration.
func TestPostgresRejectsUnownedSchema(t *testing.T) {
	uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
	if uri == "" {
		t.Skip("PostgreSQL fixture not configured")
	}
	if db, err := database.Open(context.Background(), database.Config{Profile: "postgres", PostgresURI: uri, Schema: "pg_catalog"}); err == nil {
		db.Close()
		t.Error("Open(system-owned schema) succeeded, want ownership denial")
	}
}

// TestPostgresOwnershipFailsBeforeMigration refuses insecure or unowned production targets.
func TestPostgresOwnershipFailsBeforeMigration(t *testing.T) {
	for _, cfg := range []database.Config{
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=disable", Schema: "shiftbox"},
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=verify-full", Schema: "public"},
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=verify-full", Schema: "dex"},
	} {
		if db, err := database.Open(context.Background(), cfg); err == nil {
			db.Close()
			t.Error("Open(unsafe postgres config) succeeded, want validation failure")
		} else if strings.Contains(err.Error(), "secret-password") {
			t.Error("Open error disclosed database credentials")
		}
	}
}
