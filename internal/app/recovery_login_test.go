package app

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
)

// TestRecoveryLoginIssuesOnlyRestrictedAdministratorSessions checks the local
// Dex session issuer against recovery and active installation states.
func TestRecoveryLoginIssuesOnlyRestrictedAdministratorSessions(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		checkRecoveryLogin(t, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "login.db")})
	})
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if uri == "" {
			t.Skip("PostgreSQL fixture not configured")
		}
		checkRecoveryLogin(t, database.Config{Profile: "postgres", Schema: "shiftbox_test", PostgresURI: uri})
	})
}

func checkRecoveryLogin(t *testing.T, cfg database.Config) {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if cfg.Profile == "postgres" {
		// Shadow durable tables with the real column types on one connection.
		ddl, err := migrations.Files.ReadFile("postgres/00001_foundation.sql")
		if err != nil {
			t.Fatal(err)
		}
		timeoutDDL, err := migrations.Files.ReadFile("postgres/00039_session_timeout.sql")
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(ddl), "-- +goose Down")
		timeoutUp, _, _ := strings.Cut(string(timeoutDDL), "-- +goose Down")
		if _, err := db.ExecContext(ctx, strings.ReplaceAll(up, "CREATE TABLE ", "CREATE TEMPORARY TABLE ")+timeoutUp); err != nil {
			t.Fatal(err)
		}
	} else if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	installation, administrator, member := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'recovery','Fixture',$2,'https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','v1','v1','now','now',1)`, installation, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin@example.test','administrator','active','local','setup','now','now',1),
		($2,'member@example.test','member','active','local','setup','now','now',1)`, administrator, member); err != nil {
		t.Fatal(err)
	}
	issue := func(id string) (auth.Session, string, error) {
		return issueControllerSession(ctx, db, installation, id, now, uuid.NewString)
	}
	if _, _, err := issue(member); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("member recovery sign-in = %v; want rejection", err)
	}
	session, token, err := issue(administrator)
	if err != nil || session.Mode != "recovery_review" {
		t.Fatalf("administrator recovery sign-in mode = %q, %v; want recovery_review", session.Mode, err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		p, err := auth.NewStore(db).Principal(ctx, tx, token)
		if err != nil {
			return err
		}
		if !auth.Authorize(p, "recovery.review", auth.Resource{}, nil, now).Allowed ||
			auth.Authorize(p, "workspace.list", auth.Resource{}, nil, now).Allowed ||
			auth.Authorize(p, "recovery.activate", auth.Resource{}, nil, now).Allowed {
			t.Error("stored recovery session did not enforce review-only authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation); err != nil {
		t.Fatal(err)
	}
	session, _, err = issue(administrator)
	if err != nil || session.Mode != "normal" || !session.ExpiresAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("sign-in after activation = %q, expires %v, %v; want normal with 30 minutes", session.Mode, session.ExpiresAt, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET session_timeout_value=2, session_timeout_unit='days' WHERE id=$1", installation); err != nil {
		t.Fatal(err)
	}
	session, _, err = issue(administrator)
	if err != nil || !session.ExpiresAt.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("sign-in after timeout change expires %v, %v; want two days", session.ExpiresAt, err)
	}
}
