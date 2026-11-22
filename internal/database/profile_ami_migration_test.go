package database

import (
	"database/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProfileAMIMigration backfills only an unambiguous historical EC2 image;
// ambiguous, unused, and UTM profiles survive without an invented AMI choice.
func TestProfileAMIMigration(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) { testProfileAMIMigration(t, profile) })
	}
}

func testProfileAMIMigration(t *testing.T, profile string) {
	t.Helper()
	cfg := Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "profile-images.db")}
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
		// Use a fresh owned schema; another test may already have migrated
		// shiftbox_test to the latest version before this upgrade test runs.
		cfg.Schema = "shiftbox_test_ami_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+cfg.Schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec("DROP SCHEMA " + cfg.Schema + " CASCADE"); err != nil {
				t.Errorf("remove migration schema: %v", err)
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
	if _, err := migrator.UpTo(t.Context(), 32); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	 VALUES('owner','owner@example.test','administrator','active','local','setup','now','now',1);
	INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES
	 ('one','One',2,4096,32768,1,1),('ambiguous','Ambiguous',2,4096,32768,1,1),
	 ('utm','UTM',2,1024,40,1,1),('unused','Unused',2,4096,32768,1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, kind, image string }{{"a", "ec2", `{"amiId":"ami-a"}`}, {"b", "ec2", `{"amiId":"ami-b"}`}, {"u", "utm", ""}} {
		_, err = db.Exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		 owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,provider_kind,ec2_image_json)
		 VALUES($1,$1,'amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture','owner','now','available',$2,$3)`, item.id, item.kind, item.image)
		if err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	for _, item := range []struct{ id, profile, image string }{{"t1", "one", "a"}, {"t2", "one", "a"}, {"t3", "ambiguous", "a"}, {"t4", "ambiguous", "b"}, {"t5", "utm", "u"}} {
		_, err = tx.Exec(`INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
		 VALUES($1,'owner',$1,'active',1,'now','now',1)`, item.id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(`INSERT INTO template_revisions(template_id,revision,repository_url,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at)
		 VALUES($1,1,'https://github.com/example/repo','.devcontainer/devcontainer.json',$2,$3,'[]','owner','now')`, item.id, item.profile, item.image)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db, profile); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, want string }{{"one", "a"}, {"ambiguous", ""}, {"utm", ""}, {"unused", ""}} {
		var got string
		if err := db.QueryRow(`SELECT COALESCE(platform_artifact_id,'') FROM resource_profiles WHERE id=$1`, item.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != item.want {
			t.Errorf("migrated profile %q AMI=%q, want %q", item.id, got, item.want)
		}
	}
}
