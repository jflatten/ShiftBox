package contract_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
)

// TestAuthoritySchemaConstraints rejects malformed installation identity, raw credentials, and invalid grants in both dialects.
func TestAuthoritySchemaConstraints(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "constraints.db"), Schema: "shiftbox_test"}
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
			// Connection-local tables test the exact current definitions without
			// resetting the operator-owned PostgreSQL fixture or its migration history.
			if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(ddl), "CREATE TABLE ", "CREATE TEMPORARY TABLE ")); err != nil {
				t.Fatal(err)
			}
			installation := `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES($1,'configuring','ShiftBox','sqlite',$2,'{workspace}.ide.test','{workspace}-{service}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`
			for _, id := range []any{nil, "", "installation", "00000000-0000-4000-8000-00000000000g", "000000000000-4000-8000-000000000001"} {
				if _, err := db.ExecContext(ctx, installation, id, "https://management.test"); err == nil {
					t.Errorf("installation constraint accepted malformed UUID %v", id)
					// Remove unexpected successes so the singleton constraint cannot mask later failures.
					if _, err := db.ExecContext(ctx, `DELETE FROM installations`); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := db.ExecContext(ctx, installation, "00000000-0000-4000-8000-000000000001", "https://management.test"); err != nil {
				t.Fatalf("insert valid installation: %v", err)
			}
			if _, err := db.ExecContext(ctx, installation, "00000000-0000-4000-8000-000000000002", "https://other.test"); err == nil {
				t.Error("schema accepted a second installation with a distinct origin")
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version) VALUES('actor','actor@example.test','administrator','active','local','setup','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`); err != nil {
				t.Fatal(err)
			}
			digest := strings.Repeat("a", 64)
			insert := `INSERT INTO sessions(id,token_digest,user_id,mode,csrf_digest,authenticated_at,expires_at) VALUES($1,$2,'actor','normal',$3,'2026-11-14T00:00:00Z','2026-11-14T01:00:00Z')`
			if _, err := db.ExecContext(ctx, insert, "valid", digest, digest); err != nil {
				t.Fatalf("insert valid session: %v", err)
			}
			for i, pair := range [][2]string{{strings.Repeat("b", 43), digest}, {strings.Repeat("z", 64), digest}, {strings.Repeat("c", 64), "raw-csrf"}, {strings.Repeat("d", 64), strings.Repeat("z", 64)}} {
				if _, err := db.ExecContext(ctx, insert, fmt.Sprintf("invalid-%d", i), pair[0], pair[1]); err == nil {
					t.Errorf("session constraint accepted invalid credential representation case %d", i)
				}
			}
			grant := `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at) VALUES($1,'actor',$2,'resource',$3,'actor','2026-11-14T00:00:00Z')`
			if _, err := db.ExecContext(ctx, grant, "valid", "workspace", "workspace.connect_admin"); err != nil {
				t.Fatalf("insert valid grant: %v", err)
			}
			for i, pair := range [][2]string{{"workspace", "workspace.superuser"}, {"template", "workspace.connect_admin"}, {"workspace", "template.view"}} {
				if _, err := db.ExecContext(ctx, grant, fmt.Sprintf("invalid-%d", i), pair[0], pair[1]); err == nil {
					t.Errorf("grant constraint accepted kind/action %q/%q", pair[0], pair[1])
				}
			}
		})
	}
}
