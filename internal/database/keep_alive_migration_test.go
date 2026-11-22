package database

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestKeepAliveMigration upgrades an installation and running workspace from
// schema 34, adding the selected defaults without changing retained lifecycle,
// resource, activity, or destructive-retention settings.
func TestKeepAliveMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			cfg := Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "keep-alive.db")}
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
				cfg.Schema = "shiftbox_test_keep_alive_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+cfg.Schema); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := admin.Exec("DROP SCHEMA " + cfg.Schema + " CASCADE"); err != nil {
						t.Errorf("remove keep-alive migration schema: %v", err)
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
			if _, err := migrator.UpTo(t.Context(), 34); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO installations
				(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,
				active_release,min_guest_release,created_at,updated_at,version)
				VALUES('00000000-0000-4000-8000-000000000035','active','ShiftBox',$1,'https://management.test','{workspace}.ide.test',
				'{service}--{workspace}.preview.test','test','test','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',9)`, profile); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{
				`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
				VALUES('owner','owner@example.test','member','active','local','setup','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`,
				`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
				owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
				VALUES('artifact','test','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture','owner','2026-11-14T00:00:00Z','available')`,
				`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
				repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
				desired_state,observed_state,generation,ownership_marker,provider_instance_id,retention_seconds,retention_deadline,
				last_activity_at,created_at,updated_at,version)
				VALUES('workspace','00000000-0000-4000-8000-000000000035','owner','template',1,'artifact',
				'https://github.com/example/project','devcontainer.json',4,8192,32768,'{"mode":"shared"}',
				'running','running',3,'owned','vm',7200,'2026-11-14T03:00:00Z',
				'2026-11-14T01:00:00Z','2026-11-14T00:00:00Z','2026-11-14T01:00:00Z',7)`,
			} {
				if _, err := db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(t.Context(), db, profile); err != nil {
				t.Fatal(err)
			}
			var maximum, defaultMinutes, policyVersion, installationVersion int64
			var encoded string
			if err := db.QueryRowContext(t.Context(), `SELECT keep_alive_maximum_minutes,keep_alive_allowed_minutes_json,
				keep_alive_default_minutes,keep_alive_version,version FROM installations WHERE singleton=1`).
				Scan(&maximum, &encoded, &defaultMinutes, &policyVersion, &installationVersion); err != nil {
				t.Fatal(err)
			}
			var choices []int64
			if err := json.Unmarshal([]byte(encoded), &choices); err != nil {
				t.Fatal(err)
			}
			if maximum != 0 || defaultMinutes != 30 || policyVersion != 1 || installationVersion != 9 ||
				!slices.Equal(choices, []int64{30, 60, 120, 240, 480, 720, 0}) {
				t.Errorf("migrated keep-alive policy = max %d, choices %v, default %d, revision %d, installation revision %d; want 0/[30 60 120 240 480 720 0]/30/1/9",
					maximum, choices, defaultMinutes, policyVersion, installationVersion)
			}
			var override sql.NullInt64
			var desired, observed, instance, activity, deadline, created, updated string
			var cpu, memory, disk, generation, retention, version int64
			if err := db.QueryRowContext(t.Context(), `SELECT keep_alive_minutes,desired_state,observed_state,provider_instance_id,
				last_activity_at,retention_deadline,created_at,updated_at,cpu_count,memory_mib,disk_mib,generation,retention_seconds,version
				FROM workspaces WHERE id='workspace'`).Scan(&override, &desired, &observed, &instance, &activity, &deadline, &created, &updated,
				&cpu, &memory, &disk, &generation, &retention, &version); err != nil {
				t.Fatal(err)
			}
			if override.Valid || desired != "running" || observed != "running" || instance != "vm" || cpu != 4 || memory != 8192 || disk != 32768 ||
				generation != 3 || retention != 7200 || version != 7 || activity != "2026-11-14T01:00:00Z" || deadline != "2026-11-14T03:00:00Z" ||
				created != "2026-11-14T00:00:00Z" || updated != "2026-11-14T01:00:00Z" {
				t.Errorf("migration changed workspace: override=%v state=%s/%s instance=%s resources=%d/%d/%d generation=%d retention=%d deadline=%s activity=%s created=%s updated=%s version=%d; want inherited keep-alive and unchanged schema-34 values",
					override, desired, observed, instance, cpu, memory, disk, generation, retention, deadline, activity, created, updated, version)
			}
			if err := CheckSchema(t.Context(), db, profile); err != nil {
				t.Errorf("CheckSchema after keep-alive migration = %v, want valid", err)
			}
		})
	}
}
