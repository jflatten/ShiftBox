package backup

import (
	"context"
	"database/sql"
	"errors"
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
