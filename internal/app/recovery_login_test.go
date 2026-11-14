package app

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestRecoveryLoginIssuesOnlyRestrictedAdministratorSessions checks the local
// Dex session issuer against recovery and active installation states.
func TestRecoveryLoginIssuesOnlyRestrictedAdministratorSessions(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "login.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	installation, administrator, member := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'recovery','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','v1','v1','now','now',1)`, installation); err != nil {
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
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active'"); err != nil {
		t.Fatal(err)
	}
	session, _, err = issue(administrator)
	if err != nil || session.Mode != "normal" {
		t.Fatalf("sign-in after activation = %q, %v; want normal", session.Mode, err)
	}
}
