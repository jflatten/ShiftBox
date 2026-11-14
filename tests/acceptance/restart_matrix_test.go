package acceptance_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestRestartInitialAdministrator resumes both persisted setup steps after
// losing a controller before and after the corresponding executor effect.
func TestRestartInitialAdministrator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		step  int
		apply bool
	}{
		{name: "administrator before observation", step: 0},
		{name: "administrator after observation", step: 0, apply: true},
		{name: "credentials before Dex write", step: 1},
		{name: "credentials after Dex write", step: 1, apply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkRestartInitialAdministrator(t, tc.step, tc.apply)
		})
	}
}

func checkRestartInitialAdministrator(t *testing.T, step int, apply bool) {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "setup.db")
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	seed := database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "Restart fixture", Origin: "https://127.0.0.1:8443",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "fixture", MinGuestRelease: "fixture", Now: now,
	}
	open := func() *sql.DB {
		t.Helper()
		db, _, err := database.OpenBootstrap(ctx, path, seed)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	journal := operation.NewJournal(db, clock, uuid.NewString)
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	intent := app.AdministratorIntent{InstallationID: seed.ID, UserID: uuid.NewString(),
		Email: "setup-restart@example.test", CreatedAt: now}
	admission, err := app.BeginAdministrator(ctx, db, journal, intent, cipher, "initial-owner-restart")
	if err != nil {
		t.Fatal(err)
	}
	dex := &restartPasswordStore{}
	executor := &app.AdministratorExecutor{DB: db, InstallationID: seed.ID, Admin: dex}
	worker := operation.Worker{Journal: journal, Name: "first-controller", LeaseDuration: time.Minute, Executor: executor}
	if step == 1 {
		if err := worker.RunOnce(ctx, admission.Operation.ID); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := journal.Claim(ctx, admission.Operation.ID, worker.Name, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, step); err != nil {
		t.Fatal(err)
	}
	before, err := journal.Get(ctx, admission.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if apply {
		if _, err := executor.Apply(ctx, before, before.Steps[step]); err != nil {
			t.Fatal(err)
		}
	}
	var oldHash []byte
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM bootstrap_administrator").Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	reopened := open()
	restartedJournal := operation.NewJournal(reopened, clock, uuid.NewString)
	intent.UserID = uuid.NewString()
	retry, err := app.BeginAdministrator(ctx, reopened, restartedJournal, intent, cipher, "retry-owner-after-restart")
	if err != nil || retry.AdministratorID != admission.AdministratorID || retry.Operation.ID != admission.Operation.ID {
		t.Fatalf("restarted setup changed durable administrator or operation identity: %v", err)
	}
	finished, err := app.FinishAdministrator(ctx, reopened, restartedJournal, seed.ID, dex, retry.Operation)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("setup after restart = %q (%v), want succeeded", finished.Status, err)
	}
	for _, step := range finished.Steps {
		if step.Status != "succeeded" {
			t.Errorf("setup step %s = %s, want succeeded", step.Key, step.Status)
		}
	}
	if dex.writes != 1 || dex.userID != admission.AdministratorID || !bytes.Equal(oldHash, dex.hash) {
		t.Error("resuming setup changed the original credential or repeated the Dex write")
	}
}

type restartPasswordStore struct {
	writes int
	userID string
	hash   []byte
}

// TestRestartInterruptsIncompleteBackup starts from each durable backup state
// and checks that a new controller can admit a fresh backup without treating
// an interrupted checkpoint or encryption step as verified evidence.
func TestRestartInterruptsIncompleteBackup(t *testing.T) {
	for _, state := range []string{"preparing", "checkpointing", "encrypting"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "backup.db")}
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
				t.Fatal(err)
			}
			schema, err := database.CurrentSchemaVersion(cfg.Profile)
			if err != nil {
				t.Fatal(err)
			}
			metadata := backup.ArchiveMetadata{InstallationID: uuid.NewString(), Release: "fixture", Profile: cfg.Profile, Schema: schema}
			if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
				ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
				VALUES($1,'active','Restart fixture','sqlite','https://management.test','{workspace}.ide.test',
				'{service}--{workspace}.preview.test','fixture','fixture',$2,$2,1)`,
				metadata.InstallationID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			journal := operation.NewJournal(db, time.Now, uuid.NewString)
			service := backup.NewService(db, journal, metadata)
			op, err := service.QueueLocal(ctx, "first-backup-after-admission")
			if err != nil {
				t.Fatal(err)
			}
			if state != "preparing" {
				claim, err := journal.Claim(ctx, op.ID, "management-backup", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
					if err := journal.StartStepInTx(ctx, tx, claim, 0); err != nil {
						return err
					}
					if state == "encrypting" {
						if err := journal.CompleteStepInTx(ctx, tx, claim, 0, "Checkpoint captured."); err != nil {
							return err
						}
						if err := journal.StartStepInTx(ctx, tx, claim, 1); err != nil {
							return err
						}
					}
					_, err := tx.ExecContext(ctx, "UPDATE backup_records SET state=$1 WHERE operation_id=$2", state, op.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restartedJournal := operation.NewJournal(reopened, time.Now, uuid.NewString)
			restarted := backup.NewService(reopened, restartedJournal, metadata)
			if err := restarted.InterruptIncomplete(ctx); err != nil {
				t.Fatal(err)
			}
			var backupState, code string
			if err := reopened.QueryRowContext(ctx, "SELECT state,error_code FROM backup_records WHERE operation_id=$1",
				op.ID).Scan(&backupState, &code); err != nil || backupState != "failed" || code != "backup.interrupted" {
				t.Errorf("backup after restart = %q/%q (%v), want failed/backup.interrupted", backupState, code, err)
			}
			if got, err := restartedJournal.Get(ctx, op.ID); err != nil || got.Status != "failed" {
				t.Errorf("backup operation after restart = %q (%v), want failed", got.Status, err)
			}
			if ok, err := backup.HasCompletedBackup(ctx, reopened, metadata); err != nil || ok {
				t.Errorf("completed backup after interrupted %s = %t (%v), want false", state, ok, err)
			}
			fresh, err := restarted.QueueLocal(ctx, "new-backup-after-restart")
			if err != nil || fresh.ID == op.ID || fresh.Status != "pending" {
				t.Errorf("new backup after restart = %q (%v), want another pending operation", fresh.Status, err)
			}
		})
	}
}

func (s *restartPasswordStore) Ensure(_ context.Context, _ string, userID string, hash []byte) error {
	s.writes++
	s.userID = userID
	s.hash = bytes.Clone(hash)
	return nil
}
