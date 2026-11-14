package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLiteCheckpointCapturesWAL verifies that a checkpoint includes a
// committed write still held in the source WAL and excludes later writes.
func TestSQLiteCheckpointCapturesWAL(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE records (value TEXT)", "INSERT INTO records VALUES ('captured')"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "checkpoint.db")
	if err := SQLiteCheckpoint(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO records VALUES ('after checkpoint')"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var count int
	var value string
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*),max(value) FROM records").Scan(&count, &value); err != nil || count != 1 || value != "captured" {
		t.Fatalf("SQLite checkpoint = %d %q, %v; want only captured record", count, value, err)
	}
	if err := SQLiteCheckpoint(ctx, db, path); err == nil {
		t.Fatal("checkpoint replaced an existing snapshot")
	}
}

// TestCaptureCheckpointResumesBeforeAssembly verifies that the write fence
// covers application and Dex acquisition and releases before archive assembly.
func TestCaptureCheckpointResumesBeforeAssembly(t *testing.T) {
	var writes sync.Mutex
	var events []string
	pause := func(context.Context) (func(), error) {
		writes.Lock()
		events = append(events, "pause")
		return func() {
			events = append(events, "resume")
			writes.Unlock()
		}, nil
	}
	capture := func(context.Context) error {
		for _, store := range []string{"application", "Dex"} {
			events = append(events, store)
			if writes.TryLock() {
				writes.Unlock()
				t.Errorf("captureCheckpoint allowed a management write during %s acquisition", store)
			}
		}
		return nil
	}
	if err := captureCheckpoint(t.Context(), pause, capture); err != nil {
		t.Fatalf("captureCheckpoint with writable stores = %v, want success", err)
	}
	if !writes.TryLock() {
		t.Error("captureCheckpoint kept management writes paused at archive assembly")
	} else {
		writes.Unlock()
	}
	events = append(events, "assemble")
	want := []string{"pause", "application", "Dex", "resume", "assemble"}
	if !slices.Equal(events, want) {
		t.Errorf("captureCheckpoint and assembly event order = %v, want %v", events, want)
	}
}

// TestCaptureCheckpointReleasesAfterFailure verifies that failed identity
// checkpoints release management writes, including when the caller cancels.
func TestCaptureCheckpointReleasesAfterFailure(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "write failure", err: errors.New("Dex checkpoint output unavailable")},
		{name: "cancelled", err: context.Canceled},
	} {
		t.Run(failure.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			resumed := 0
			pause := func(context.Context) (func(), error) {
				return func() { resumed++ }, nil
			}
			capture := func(ctx context.Context) error {
				if errors.Is(failure.err, context.Canceled) {
					cancel()
					return ctx.Err()
				}
				return failure.err
			}
			err := captureCheckpoint(ctx, pause, capture)
			if !errors.Is(err, failure.err) || resumed != 1 {
				t.Errorf("captureCheckpoint on %s during Dex acquisition = %v, resumed %d times, want %v and one resume",
					failure.name, err, resumed, failure.err)
			}
		})
	}
}

// TestCaptureCheckpointDoesNotAcquireWithoutFence simulates a drain that
// times out before all existing management writes have finished.
func TestCaptureCheckpointDoesNotAcquireWithoutFence(t *testing.T) {
	drainErr := errors.New("pending management write did not drain")
	pause := func(context.Context) (func(), error) { return nil, drainErr }
	called := false
	capture := func(context.Context) error {
		called = true
		return nil
	}
	err := captureCheckpoint(t.Context(), pause, capture)
	if !errors.Is(err, drainErr) || called {
		t.Errorf("captureCheckpoint with an undrained write = %v, capture called %t, want drain error and no capture", err, called)
	}
}

// TestStageSQLiteCheckpoint checks that both stores and the fixed protected
// inputs are staged independently of later management writes.
func TestStageSQLiteCheckpoint(t *testing.T) {
	ctx := t.Context()
	installation, stage := t.TempDir(), checkpointStage(t)
	appDB := checkpointDatabase(t, filepath.Join(installation, "active.db"), "application")
	dexPath := filepath.Join(installation, "dex #profile.db")
	checkpointDexDatabase(t, dexPath)
	input := checkpointInputs(t, installation)
	sources, err := StageSQLiteCheckpoint(ctx, stage, appDB, dexPath, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 5 {
		t.Fatalf("got %d checkpoint entries, want 5", len(sources))
	}
	for name, want := range map[string]string{"application.db": "application", "dex.db": "identity"} {
		snapshot, err := sql.Open("sqlite", sources[name])
		if err != nil {
			t.Fatal(err)
		}
		var got string
		err = snapshot.QueryRowContext(ctx, "SELECT name FROM profile").Scan(&got)
		snapshot.Close()
		if err != nil || got != want {
			t.Errorf("snapshot %s = %q, %v; want %q", name, got, err, want)
		}
	}
	if err := os.WriteFile(input.Configuration, []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"configuration.json", "installation.key", "guest-ca.json"} {
		path := sources[name]
		content, err := os.ReadFile(path)
		if err != nil || string(content) != name || filepath.Dir(path) != stage {
			t.Fatalf("protected snapshot %s = %q, %v", name, content, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("protected snapshot %s is not private (%v)", name, err)
		}
	}
}

// TestStageSQLiteCheckpointRejectsMissingIdentity ensures a misconfigured Dex
// path never creates a new database or yields checkpoint evidence.
func TestStageSQLiteCheckpointRejectsMissingIdentity(t *testing.T) {
	installation := t.TempDir()
	db := checkpointDatabase(t, filepath.Join(installation, "active.db"), "application")
	dexPath := filepath.Join(installation, "missing.db")
	sources, err := StageSQLiteCheckpoint(t.Context(), checkpointStage(t), db, dexPath, checkpointInputs(t, installation))
	if err == nil || len(sources) > 0 {
		t.Fatalf("staged checkpoint with missing Dex: %v", err)
	}
	if _, err := os.Lstat(dexPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing identity was created or couldn't be checked: %v", err)
	}
}

// TestStageSQLiteCheckpointRejectsUnprotectedInput ensures outside files and
// openly readable keys cannot enter the protected archive.
func TestStageSQLiteCheckpointRejectsUnprotectedInput(t *testing.T) {
	installation := t.TempDir()
	db := checkpointDatabase(t, filepath.Join(installation, "active.db"), "application")
	dexPath := filepath.Join(installation, "dex.db")
	checkpointDexDatabase(t, dexPath)
	input := checkpointInputs(t, installation)
	if err := os.Chmod(input.InstallationKey, 0644); err != nil {
		t.Fatal(err)
	}
	if sources, err := StageSQLiteCheckpoint(t.Context(), checkpointStage(t), db, dexPath, input); err == nil || len(sources) > 0 {
		t.Fatalf("staged checkpoint with a public key file: %v", err)
	}
	if err := os.Remove(input.InstallationKey); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(input.GuestCA, input.InstallationKey); err != nil {
		t.Fatal(err)
	}
	if sources, err := StageSQLiteCheckpoint(t.Context(), checkpointStage(t), db, dexPath, input); err == nil || len(sources) > 0 {
		t.Fatalf("staged checkpoint with a symlink as a protected file: %v", err)
	}
}

func checkpointDatabase(t *testing.T, path, label string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE profile (name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO profile VALUES ($1)", label); err != nil {
		t.Fatal(err)
	}
	return db
}

func checkpointInputs(t *testing.T, dir string) ProtectedInputs {
	t.Helper()
	for _, name := range []string{"configuration.json", "installation.key", "guest-ca.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return ProtectedInputs{
		Configuration:   filepath.Join(dir, "configuration.json"),
		InstallationKey: filepath.Join(dir, "installation.key"),
		GuestCA:         filepath.Join(dir, "guest-ca.json"),
	}
}

func checkpointStage(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.Chmod(stage, 0700); err != nil {
		t.Fatal(err)
	}
	return stage
}

// TestStageSQLiteCheckpointRejectsUninitializedIdentity checks that an
// unrelated, valid SQLite database cannot stand in for the Dex store.
func TestStageSQLiteCheckpointRejectsUninitializedIdentity(t *testing.T) {
	installation := t.TempDir()
	db := checkpointDatabase(t, filepath.Join(installation, "active.db"), "application")
	dexPath := filepath.Join(installation, "dex.db")
	checkpointDatabase(t, dexPath, "unrelated")
	sources, err := StageSQLiteCheckpoint(t.Context(), checkpointStage(t), db, dexPath, checkpointInputs(t, installation))
	if err == nil || len(sources) > 0 {
		t.Fatalf("staged checkpoint with an uninitialized Dex database: %v", err)
	}
}

func checkpointDexDatabase(t *testing.T, path string) {
	t.Helper()
	db := checkpointDatabase(t, path, "identity")
	for _, sql := range []string{
		"CREATE TABLE migrations (num INTEGER NOT NULL, at TEXT NOT NULL)",
		"INSERT INTO migrations VALUES (1, 'now')",
		"CREATE TABLE password (email TEXT, user_id TEXT, hash BLOB)",
	} {
		if _, err := db.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSQLiteCheckpointLocksDexWrites checks that a separate connection cannot
// change Dex while the application snapshot and protected files are captured.
func TestSQLiteCheckpointLocksDexWrites(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "dex.db")
	checkpointDexDatabase(t, path)
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	dex, release, err := lockSQLiteDex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	insert := "INSERT INTO profile VALUES ('after cutoff')"
	if _, err := writer.ExecContext(ctx, insert); err == nil {
		t.Fatal("separate Dex writer committed during the checkpoint")
	}
	snapshot := filepath.Join(checkpointStage(t), "dex.db")
	if err := SQLiteCheckpoint(ctx, dex, snapshot); err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := writer.ExecContext(ctx, insert); err != nil {
		t.Fatalf("Dex writer did not resume: %v", err)
	}
	readback, err := sql.Open("sqlite", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer readback.Close()
	var rows int
	if err := readback.QueryRowContext(ctx, "SELECT count(*) FROM profile").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("Dex checkpoint row count = %d, %v; want 1", rows, err)
	}
}
