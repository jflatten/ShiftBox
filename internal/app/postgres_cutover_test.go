package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestPostgresCutoverResumesAfterSourceRetirement checks ordering, session
// retirement, durable activation receipts, and replay after a failed target commit.
func TestPostgresCutoverResumesAfterSourceRetirement(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	const actor = "owner"
	const key = "activate-postgres-test"
	const id = "719b54c3-6e15-499c-a0ed-407e1b316a90"
	dir := t.TempDir()
	openDB := func(name string) *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, name)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	open := func(name, profile string) *sql.DB {
		t.Helper()
		db := openDB(name)
		if err := database.Migrate(ctx, db, "sqlite"); err != nil {
			t.Fatal(err)
		}
		execCutover(t, db, `INSERT INTO installations(id,state,data_profile,organization_name,management_origin,
			ide_host_pattern,preview_host_pattern,active_release,min_guest_release,config_generation,created_at,updated_at,version)
			VALUES($1,'configuring',$2,'ShiftBox','https://manage.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','test','test',1,'now','now',1)`, id, profile)
		execCutover(t, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,
			created_by,created_at,updated_at,version) VALUES('owner','owner@example.test','administrator','active','local','setup','now','now',1)`)
		return db
	}
	source, target := open("bootstrap.db", "sqlite"), open("target.db", "postgres")
	execCutover(t, source, `INSERT INTO bootstrap_administrator(installation_id,user_id,password_hash,credential_ready,delivered_at,created_at)
		VALUES($1,$2,'fixture',1,'now','now')`, id, actor)
	session, _, err := auth.NewSession(uuid.NewString(), actor, "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, source, func(tx *sql.Tx) error {
		return auth.NewStore(source).SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	state := func(db *sql.DB, want string) {
		t.Helper()
		var actual string
		err := db.QueryRowContext(ctx, "SELECT state FROM installations WHERE id=$1", id).Scan(&actual)
		if err != nil || actual != want {
			t.Fatalf("installation state = %s, %v, want %s", actual, err, want)
		}
	}
	receipts := 0
	input := postgresCutover{
		source: operation.NewJournal(source, clock, uuid.NewString),
		target: operation.NewJournal(target, clock, uuid.NewString),
		id:     id, actor: actor, key: key, now: now,
		validate: func(context.Context, *sql.Tx) error { return nil },
		onActivated: func() {
			state(source, "blocked")
			state(target, "active")
			receipts++
		},
	}
	refused := errors.New("fixture validation failure")
	invalid := input
	invalid.validate = func(context.Context, *sql.Tx) error { return refused }
	if _, err := invalid.run(ctx, source, target); !errors.Is(err, refused) {
		t.Fatalf("invalid target returned %v, want readiness failure", err)
	}
	state(source, "configuring")
	state(target, "configuring")
	execCutover(t, target, `CREATE TRIGGER fail_activation BEFORE UPDATE ON installations WHEN NEW.state='active'
		BEGIN SELECT RAISE(ABORT,'fixture activation failure'); END`)
	if _, err := input.run(ctx, source, target); err == nil {
		t.Fatal("expected target activation failure")
	}
	state(source, "blocked")
	state(target, "configuring")
	if receipts != 0 {
		t.Fatalf("failed activation emitted %d durable receipts, want none", receipts)
	}
	var reason string
	if err := source.QueryRowContext(ctx, "SELECT revocation_reason FROM sessions WHERE revoked_at IS NOT NULL").Scan(&reason); err != nil || reason != "setup_activated" {
		t.Fatalf("source session retirement = %s, %v, want setup_activated", reason, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, _, err := database.OpenBootstrap(ctx, filepath.Join(dir, "bootstrap.db"), database.BootstrapSeed{}); !errors.Is(err, database.ErrBootstrapUnavailable) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("normal local setup reopened retired authority, error %v", err)
	}
	source, target = openDB("bootstrap.db"), openDB("target.db")
	input.source = operation.NewJournal(source, clock, uuid.NewString)
	input.target = operation.NewJournal(target, clock, uuid.NewString)
	execCutover(t, target, "DROP TRIGGER fail_activation")
	other := open("other.db", "postgres")
	if _, err := input.run(ctx, source, other); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("retired source accepted a different target, error %v", err)
	}
	state(other, "configuring")
	state(target, "configuring")
	differentRequest := input
	differentRequest.key = "different-activation-key"
	if _, err := differentRequest.run(ctx, source, target); !errors.Is(err, ErrSetupConflict) {
		t.Fatalf("different request after retirement returned %v, want setup conflict", err)
	}
	first, err := input.run(ctx, source, target)
	if err != nil || first.Status != "succeeded" || first.ID == "" {
		t.Fatalf("resumed activation status = %s, %v, want succeeded", first.Status, err)
	}
	if receipts != 1 {
		t.Errorf("verified activation emitted %d durable receipts, want 1", receipts)
	}
	replayed, err := input.run(ctx, source, target)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("activation replay returned %s, %v, want %s", replayed.ID, err, first.ID)
	}
	state(source, "blocked")
	state(target, "active")
}

func execCutover(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}
