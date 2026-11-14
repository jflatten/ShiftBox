package audit_test

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPruneHistory checks that the cutoff keeps boundary events and that
// recovery review preserves history along with current authority.
func TestPruneHistory(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "history.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO installations (id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES ('00000000-0000-4000-8000-000000000001','recovery','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','dev','dev','now','now',1)`)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES ('admin','admin@example.test','administrator','active','local','setup','now','now',1)`)
	exec(`INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
		VALUES('grant','admin','workspace','workspace','workspace.connect_admin','admin','old')`)
	exec(`INSERT INTO operations(id,type,generation,requested_by,request_key,status,created_at,version)
		VALUES('operation','stop',1,'admin','stop-operation-fixture','waiting','old',1)`)
	exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','admin','now','available')`)
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
		generation,ownership_marker,created_at,updated_at,version)
		SELECT 'workspace',id,'admin','template',2,'artifact','https://github.com/example/repo','devcontainer.json',
		1,512,40,'{}','stopped','stopped',1,'owned','now','now',1 FROM installations`)
	cutoff := time.Date(2026, 8, 16, 0, 0, 0, 20, time.UTC)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, stamp := range []time.Time{cutoff.Truncate(time.Second), cutoff.Add(-10), cutoff, cutoff.Add(10)} {
			if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: stamp, ActorType: "user", ActorID: "admin",
				Action: "workspace.connect_admin", ResourceType: "workspace", ResourceID: "workspace",
				Outcome: "allowed", ReasonCode: "explicit_grant", Metadata: audit.Metadata{GrantID: "grant"}}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prune := func(want int64) {
		t.Helper()
		got, err := audit.Prune(ctx, db, cutoff)
		if err != nil || got != want {
			t.Fatalf("Prune() = %d, %v, want %d", got, err, want)
		}
	}
	prune(0)
	exec("UPDATE installations SET state='active' WHERE singleton=1")
	prune(2)
	prune(0)
	var events, grants, operations, users, provenance int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM audit_events),
		(SELECT count(*) FROM resource_grants WHERE revoked_at IS NULL),
		(SELECT count(*) FROM operations WHERE status='waiting'),
		(SELECT count(*) FROM users WHERE membership_state='active'),
		(SELECT count(*) FROM workspaces WHERE template_id='template' AND template_revision=2 AND platform_artifact_id='artifact')`).
		Scan(&events, &grants, &operations, &users, &provenance); err != nil {
		t.Fatal(err)
	}
	if events != 2 || grants != 1 || operations != 1 || users != 1 || provenance != 1 {
		t.Errorf("after prune = %d events, %d grants, %d operations, %d users, %d provenance records, want 2/1/1/1/1", events, grants, operations, users, provenance)
	}
}

// TestAuditAppendAndRollback proves records share the protected intent transaction and reject secret-bearing metadata at the schema boundary.
func TestAuditAppendAndRollback(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "audit.db"), Schema: "shiftbox_test"}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL fixture not configured")
				}
			}
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ddl, err := migrations.Files.ReadFile(profile + "/00001_foundation.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(ddl), "CREATE TABLE ", "CREATE TEMPORARY TABLE ")); err != nil {
				t.Fatal(err)
			}
			event := audit.Event{OccurredAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), ActorType: "user", ActorID: "admin", Action: "workspace.connect_admin", ResourceType: "workspace", ResourceID: "workspace", Outcome: "allowed", ReasonCode: "explicit_grant", Metadata: audit.Metadata{OwnerID: "owner", GrantID: "grant"}}
			var first int64
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { var err error; first, err = audit.Append(ctx, tx, event); return err }); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("intent failed")
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				if _, err := audit.Append(ctx, tx, event); err != nil {
					return err
				}
				return failure
			}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			var second int64
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { var err error; second, err = audit.Append(ctx, tx, event); return err }); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM audit_events`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 || first >= second {
				t.Errorf("audit count/IDs = %d/%d/%d, want 2 and increasing IDs", count, first, second)
			}
			for _, metadata := range []string{`{"token":"private"}`, `{"ownerId":{"password":"private"}}`, `{"grantId":null}`, `[]`} {
				_, err := db.Exec(`INSERT INTO audit_events(occurred_at,actor_type,actor_id,action,resource_type,resource_id,outcome,reason_code,metadata_json) VALUES('2026-11-14T00:00:00Z','user','admin','workspace.connect_admin','workspace','workspace','allowed','explicit_grant',$1)`, metadata)
				if err == nil {
					t.Errorf("audit schema accepted metadata %s", metadata)
				}
			}
		})
	}
}
