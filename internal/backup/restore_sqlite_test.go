package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/database"
)

// TestPublishSQLiteRestorePublishesPreparedDirectory checks that the
// replacement is visible only once its database enters the recovery gate.
func TestPublishSQLiteRestorePublishesPreparedDirectory(t *testing.T) {
	root := checkpointStage(t)
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(stage, "application.db")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE state (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(filepath.Join(stage, "application.db"), 0600); err != nil {
		t.Fatal(err)
	}
	replace := filepath.Join(root, "replacement")
	prepared := 0
	prepare := func(ctx context.Context, db *sql.DB) error {
		prepared++
		if _, err := os.Lstat(replace); !errors.Is(err, os.ErrNotExist) {
			t.Error("replacement was visible before recovery state was prepared")
		}
		_, err := db.ExecContext(ctx, "INSERT INTO state VALUES ('recovery')")
		return err
	}
	if err := publishSQLiteRestore(t.Context(), stage, replace, prepare); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", filepath.Join(replace, "application.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow("SELECT value FROM state").Scan(&state); err != nil || state != "recovery" || prepared != 1 {
		t.Fatalf("published state = %q, %v; prepared %d times, want recovery", state, err, prepared)
	}
}

// TestActivateSQLiteRestoreEntersRecovery uses a real application snapshot to
// verify that the published replacement has its review and revocations.
func TestActivateSQLiteRestoreEntersRecovery(t *testing.T) {
	source, _, now := recoveryFixture(t)
	dir := checkpointStage(t)
	stage := filepath.Join(dir, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := SQLiteCheckpoint(ctx, source, filepath.Join(stage, "application.db")); err != nil {
		t.Fatal(err)
	}
	// The activation primitive treats identity and protected artifacts as
	// authenticated output from StageRestore.
	for _, name := range []string{"dex.db", "configuration.json", "guest-ca.json", "installation.key"} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "replacement")
	review, err := ActivateSQLiteRestore(ctx, stage, target, recoveryInstallation, recoveryValidation, recoveryActor, now)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(target, "application.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRowContext(ctx, "SELECT state FROM recovery_reviews WHERE id=$1", review).Scan(&state); err != nil || state != "identity_review" {
		t.Fatalf("published recovery review = %q, %v; want identity review", state, err)
	}
	assertRestoredAuthority(t, db, false)
	assertRestoredAuthority(t, source, true)
}

// TestPublishSQLiteRestoreRefusesOccupiedTarget verifies neither an existing
// empty directory nor a newly-appearing target is overwritten.
func TestPublishSQLiteRestoreRefusesOccupiedTarget(t *testing.T) {
	for _, before := range []bool{true, false} {
		root := checkpointStage(t)
		stage := filepath.Join(root, "stage")
		if err := os.Mkdir(stage, 0700); err != nil {
			t.Fatal(err)
		}
		application, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(stage, "application.db")})
		if err != nil {
			t.Fatal(err)
		}
		application.Close()
		if err := os.Chmod(filepath.Join(stage, "application.db"), 0600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "replacement")
		if before {
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
		}
		prepared := false
		err = publishSQLiteRestore(t.Context(), stage, target, func(context.Context, *sql.DB) error {
			prepared = true
			return os.Mkdir(target, 0700)
		})
		if !errors.Is(err, ErrRestoreTarget) || prepared == before {
			t.Fatalf("PublishSQLiteRestore with initially occupied=%t = %v, prepared=%t; want target rejection", before, err, prepared)
		}
		if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
			t.Fatalf("existing replacement changed: %v", err)
		}
	}
}
