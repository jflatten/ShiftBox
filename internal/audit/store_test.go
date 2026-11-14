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
