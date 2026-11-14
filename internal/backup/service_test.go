package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestBackupRecordsFollowVerifiedArchive checks admission, checkpointing, and
// terminal metadata against both a closed archive and a failed capture.
func TestBackupRecordsFollowVerifiedArchive(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testBackupRecords(t, "sqlite", "") })
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if uri == "" {
			t.Skip("requires a disposable PostgreSQL fixture")
		}
		testBackupRecords(t, "postgres", uri)
	})
}

func testBackupRecords(t *testing.T, profile, uri string) {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: profile, SQLitePath: filepath.Join(dir, "state.db"),
		PostgresURI: uri, Schema: "shiftbox_test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, profile); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	meta := ArchiveMetadata{InstallationID: uuid.NewString(), Release: "v1", Profile: profile, Schema: 19}
	service := NewService(db, journal, meta)
	check := func(want bool) {
		t.Helper()
		got, err := HasCompletedBackup(ctx, db, meta)
		if err != nil || got != want {
			t.Fatalf("HasCompletedBackup = %t, %v; want %t", got, err, want)
		}
	}
	admit := func(key string) operation.Operation {
		t.Helper()
		var op operation.Operation
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			op, err = service.Admit(ctx, tx, "local-operator", key)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	key := uuid.NewString()
	failed := admit(key)
	if replay := admit(key); replay.ID != failed.ID {
		t.Fatal("replay was admitted as another backup")
	}
	check(false)
	failedOutput := filepath.Join(dir, "failed.age")
	_, err = service.Run(ctx, failed.ID, failedOutput, "fixture password", func(context.Context, string) (map[string]string, error) {
		check(false)
		return nil, errors.New("sensitive storage detail")
	})
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Run with failed capture = %v; want sanitized failure", err)
	}
	if _, err := os.Stat(failedOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backup published an archive: %v", err)
	}
	if op, err := journal.Get(ctx, failed.ID); err != nil || op.Status != "failed" {
		t.Fatalf("failed backup operation = %s, %v; want failed", op.Status, err)
	}
	check(false)
	var code string
	if err := db.QueryRowContext(ctx, "SELECT error_code FROM backup_records WHERE operation_id=$1 AND state='failed'", failed.ID).Scan(&code); err != nil || code != "backup.checkpoint_failed" {
		t.Fatalf("failed backup record = %s, %v; want checkpoint failure", code, err)
	}
	cancelled := admit(uuid.NewString())
	attempt, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err = service.Run(attempt, cancelled.ID, filepath.Join(dir, "cancelled.age"), "fixture password",
		func(ctx context.Context, _ string) (map[string]string, error) {
			cancel()
			return nil, ctx.Err()
		})
	if err == nil {
		t.Fatal("Run succeeded after the checkpoint was cancelled")
	}
	if op, err := journal.Get(ctx, cancelled.ID); err != nil || op.Status != "failed" {
		t.Fatalf("cancelled backup operation = %s, %v; want durable failure", op.Status, err)
	}

	op := admit(uuid.NewString())
	output := filepath.Join(dir, "management.age")
	evidence, err := service.Run(ctx, op.ID, output, "fixture password", func(ctx context.Context, stage string) (map[string]string, error) {
		var state string
		err := db.QueryRowContext(ctx, "SELECT state FROM backup_records WHERE operation_id=$1", op.ID).Scan(&state)
		if err != nil || state != "checkpointing" {
			t.Fatalf("record during capture = %s, %v; want checkpointing", state, err)
		}
		check(false)
		sources := make(map[string]string)
		for _, name := range archiveNames(meta.Profile) {
			path := filepath.Join(stage, name)
			if err := os.WriteFile(path, []byte(name), 0600); err != nil {
				return nil, err
			}
			sources[name] = path
		}
		return sources, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := journal.Get(ctx, op.ID)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("verified backup operation = %s, %v; want succeeded", completed.Status, err)
	}
	check(true)
	if _, err := service.Run(ctx, op.ID, filepath.Join(dir, "replay.age"), "fixture password", func(context.Context, string) (map[string]string, error) {
		t.Fatal("a completed backup captured another archive on replay")
		return nil, nil
	}); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("Run after completion = %v; want conflict", err)
	}
	var digest string
	if err := db.QueryRowContext(ctx, "SELECT archive_digest FROM backup_records WHERE operation_id=$1 AND state='completed'", op.ID).Scan(&digest); err != nil || digest != evidence.ArchiveDigest {
		t.Fatalf("completed digest = %s, %v; want verified digest", digest, err)
	}
	meta.Schema++
	if got, err := HasCompletedBackup(ctx, db, meta); err != nil || got {
		t.Fatalf("HasCompletedBackup for another schema = %t, %v; want false", got, err)
	}
}
