package template

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestResolveLaunchRevision verifies exact revision lookup and current sharing
// rules, including revocation, archival, and immutable historical revisions.
func TestResolveLaunchRevision(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "templates.db")}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
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
			seedTemplate(t, db)
			resolve := func(user string, revision int64) (LaunchRevision, error) {
				var got LaunchRevision
				err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
					var err error
					got, err = ResolveLaunchRevision(ctx, tx, "test-template", revision, user)
					return err
				})
				return got, err
			}
			got, err := resolve("template-member", 1)
			if err != nil || got.RepositoryRef != "first" || got.Resource.OwnerID != "template-owner" || !got.Resource.LaunchGranted || got.ProfileID != "template-profile" || got.ArtifactID != "template-artifact" || len(got.SecretNames) != 1 || got.SecretNames[0] != "API_KEY" {
				t.Fatalf("ResolveLaunchRevision(member, 1) = %+v, %v, want revision one with current grant", got, err)
			}
			if _, err := resolve("template-member", 3); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("missing revision = %v, want no rows", err)
			}
			if _, err := db.Exec("UPDATE template_grants SET revoked_at='now' WHERE template_id='test-template'"); err != nil {
				t.Fatal(err)
			}
			got, err = resolve("template-member", 1)
			if err != nil || got.Resource.LaunchGranted {
				t.Errorf("revoked launch authorization = %+v, %v", got.Resource, err)
			}
			if _, err := db.Exec("UPDATE project_templates SET state='archived',archived_at='now' WHERE id='test-template'"); err != nil {
				t.Fatal(err)
			}
			got, err = resolve("template-member", 2)
			if err != nil || got.Resource.State != "archived" || got.RepositoryRef != "second" {
				t.Errorf("archived revision = %+v, %v", got, err)
			}
			for _, stmt := range []string{
				"UPDATE template_revisions SET repository_ref='changed' WHERE template_id='test-template'",
				"UPDATE project_templates SET owner_user_id='template-member' WHERE id='test-template'",
				"UPDATE project_templates SET current_revision=99 WHERE id='test-template'",
				"DELETE FROM template_revisions WHERE template_id='test-template' AND revision=2",
			} {
				if _, err := db.Exec(stmt); err == nil {
					t.Errorf("schema accepted %s", stmt)
				}
			}
		})
	}
}

func seedTemplate(t *testing.T, db *sql.DB) {
	t.Helper()
	err := database.Transaction(context.Background(), db, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version) VALUES
   ('template-owner','template-owner@example.test','administrator','active','local','setup','now','now',1),
   ('template-member','template-member@example.test','member','active','local','setup','now','now',1)`,
			`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
   VALUES('template-artifact','fixture-template-release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','template-owner','now','available')`,
			`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES('template-profile','Template profile',2,1024,40,1,1)`,
			`INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
   VALUES('test-template','template-owner','Fixture','active',2,'now','now',1)`,
			`INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at) VALUES
   ('test-template',1,'https://github.com/example/repo','first','.devcontainer/devcontainer.json','template-profile','template-artifact','["API_KEY"]','template-owner','now'),
   ('test-template',2,'https://github.com/example/repo','second','.devcontainer/devcontainer.json','template-profile','template-artifact','[]','template-owner','now')`,
			`INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
   VALUES('test-template','template-member',1,'now','template-owner','now')`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestValidSource rejects credentials, outside hosts, and paths that could
// resolve outside the workspace repository.
func TestValidSource(t *testing.T) {
	for _, tc := range []struct {
		repo string
		path string
	}{
		{"https://github.com.evil.test/owner/repo", ".devcontainer/devcontainer.json"},
		{"https://user@github.com/owner/repo", ".devcontainer/devcontainer.json"},
		{"https://github.com/owner/repo?token=fixture", ".devcontainer/devcontainer.json"},
		{"https://github.com/owner/repo", "../devcontainer.json"},
		{"https://github.com/owner/repo", ".devcontainer/../devcontainer.json"},
		{"https://github.com/owner/repo", "/devcontainer.json"},
	} {
		if ValidSource(tc.repo, tc.path) {
			t.Errorf("ValidSource(%q, %q) accepted invalid source", tc.repo, tc.path)
		}
	}
}
