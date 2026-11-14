package secret

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestSecretOverrides checks explicit empty values, fallback, revocation and
// rejection of ciphertext copied to a different scope.
func TestSecretOverrides(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "secrets.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	key, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000001"
	now := time.Now()
	for _, query := range []string{
		`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('` + installation + `','active','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release','now','now',1)`,
		`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version) VALUES
		('owner','owner@example.test','administrator','active','local','setup','now','now',1),
		('member','member@example.test','member','active','local','setup','now','now',1)`,
		`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available')`,
		`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES('profile','Small',2,1024,40,1,1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, query := range []string{
			`INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
			VALUES('template','owner','Fixture','active',1,'now','now',1)`,
			`INSERT INTO template_revisions(template_id,revision,repository_url,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at)
			VALUES('template',1,'https://github.com/example/repo','devcontainer.json','profile','artifact','[]','owner','now')`,
			`INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at) VALUES('template','member',1,'now','owner','now')`,
			`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
			devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,
			ownership_marker,provider_instance_id,created_at,updated_at,version)
			VALUES('work','` + installation + `','member','template',1,'artifact','https://github.com/example/repo',
			'devcontainer.json',2,1024,40,'{"mode":"shared"}','running','running',1,'owned','vm','now','now',1)`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(key)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, item := range []struct {
			scope Scope
			value string
		}{
			{Scope{"organization", installation}, "organization-fixture"},
			{Scope{"template", "template"}, "template-fixture"},
			{Scope{"user", "member"}, ""},
		} {
			if err := service.Set(ctx, tx, item.scope, "TOKEN", []byte(item.value), "owner", now); err != nil {
				return err
			}
		}
		if err := service.Set(ctx, tx, Scope{"workspace", "work"}, "TOKEN", nil, "owner", now); !errors.Is(err, ErrInvalidKey) {
			return errors.New("workspace scope was accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkValue := func(want string) {
		t.Helper()
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			value, _, err := service.ResolveWorkspace(ctx, tx, "work", 1, "TOKEN")
			if err != nil || string(value) != want {
				t.Fatalf("workspace secret = %q, %v, want %q", value, err, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	checkValue("")
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		items, err := service.List(ctx, tx, Scope{"user", "member"}, installation)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(items)
		if len(items) != 1 || !items[0].ExplicitEmpty || items[0].FallbackScope != "organization" || strings.Contains(string(raw), "fixture") {
			t.Fatalf("unexpected metadata: %s", raw)
		}
		return service.Delete(ctx, tx, Scope{"user", "member"}, "TOKEN", "member", now)
	}); err != nil {
		t.Fatal(err)
	}
	checkValue("template-fixture")
	if _, err := db.ExecContext(ctx, "UPDATE template_grants SET revoked_at='now'"); err != nil {
		t.Fatal(err)
	}
	checkValue("organization-fixture")
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, _, err := service.ResolveWorkspace(ctx, tx, "work", 2, "TOKEN"); !errors.Is(err, ErrWorkspaceDenied) {
			t.Fatalf("stale workspace generation = %v, want denial", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var leaked bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM secret_values WHERE ciphertext_json LIKE '%-fixture%')
		OR EXISTS(SELECT 1 FROM audit_events WHERE metadata_json LIKE '%-fixture%')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("secret plaintext stored in ciphertext or audit = %t, %v, want false", leaked, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE secret_values SET scope_id='another' WHERE scope_type='organization'"); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE secret_values SET scope_id=$1,name='COPIED' WHERE scope_type='organization'", installation); err != nil {
			return err
		}
		value, _, err := service.ResolveWorkspace(ctx, tx, "work", 1, "COPIED")
		if err == nil || len(value) != 0 {
			t.Fatal("copied ciphertext authenticated under another name")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
