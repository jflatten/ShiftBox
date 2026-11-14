package operation_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	_ "modernc.org/sqlite"
)

func journalFixture(t *testing.T) (*operation.Journal, *time.Time) {
	t.Helper()
	j, now, _, _ := journalDatabase(t)
	return j, now
}

func journalDatabase(t *testing.T) (*operation.Journal, *time.Time, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := database.Open(context.Background(), database.Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var sequence uint32
	return operation.NewJournal(db, func() time.Time { return now }, func() string { sequence++; return testsupport.ID(sequence) }), &now, db, path
}

// TestJournalReopen recovers committed progress after the database connection is lost.
func TestJournalReopen(t *testing.T) {
	j, now, db, path := journalDatabase(t)
	ctx := context.Background()
	op, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(ctx, op.ID, "before-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteStep(ctx, claim, 0, "instance exists"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	*now = now.Add(2 * time.Minute)
	restored := operation.NewJournal(reopened, func() time.Time { return *now }, func() string { return testsupport.ID(99) })
	if _, err := restored.Claim(ctx, op.ID, "after-restart", time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Status != "succeeded" || got.Steps[1].Status != "pending" {
		t.Error("database reopen lost durable step history")
	}
}

// TestCompletionRollsBackWithoutEvidence models a storage failure while committing success.
func TestCompletionRollsBackWithoutEvidence(t *testing.T) {
	j, _, db, _ := journalDatabase(t)
	ctx := context.Background()
	request := provisionRequest()
	request.Steps = request.Steps[:1]
	op, err := j.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_event BEFORE INSERT ON operation_events BEGIN SELECT RAISE(ABORT,'injected event storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteStep(ctx, claim, 0, "verified"); err == nil {
		t.Fatal("completion succeeded without stored evidence")
	}
	got, err := j.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.Steps[0].Status != "active" {
		t.Error("failed evidence write left partial success")
	}
}

// TestStepTimestamps preserves the original start time when an active step resumes.
func TestStepTimestamps(t *testing.T) {
	j, now, db, _ := journalDatabase(t)
	ctx := context.Background()
	op, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	started := *now
	*now = now.Add(time.Second)
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteStep(ctx, claim, 0, "verified"); err != nil {
		t.Fatal(err)
	}
	var start, finish sql.NullString
	if err := db.QueryRow(`SELECT started_at,finished_at FROM operation_steps WHERE operation_id=$1 AND step_index=0`, op.ID).Scan(&start, &finish); err != nil {
		t.Fatal(err)
	}
	if !start.Valid || !finish.Valid {
		t.Fatal("step timestamps are missing")
	}
	got, err := time.Parse(time.RFC3339Nano, start.String)
	if err != nil || !got.Equal(started) {
		t.Errorf("step start = %q, %v; want original %v", start.String, err, started)
	}
}

func provisionRequest() operation.Request {
	return operation.Request{Actor: "owner", Key: "launch-request-0001", Type: "provision", Generation: 1, Steps: []operation.Step{{Key: "create", Label: "Create VM"}, {Key: "ready", Label: "Verify readiness"}}}
}

// TestIntentSharesAuthorizationTransaction prevents intent surviving a failed admission commit.
func TestIntentSharesAuthorizationTransaction(t *testing.T) {
	j, _, db, _ := journalDatabase(t)
	ctx := context.Background()
	failure := errors.New("reservation admission failed")
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := j.CreateInTx(ctx, tx, provisionRequest()); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("admission transaction = %v, want injected failure", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("failed admission left %d operations, want 0", count)
	}
}

// TestIdempotentJournal prevents duplicate provider work after a lost HTTP response.
func TestIdempotentJournal(t *testing.T) {
	j, _ := journalFixture(t)
	ctx := context.Background()
	first, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("retry operation = %s, want %s", second.ID, first.ID)
	}
	request := provisionRequest()
	request.Actor = "another-owner"
	other, err := j.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Error("idempotency key crossed actor boundary")
	}
}

// TestLeaseRecoveryFencesStaleWorker rejects expired claims and preserves completed steps.
func TestLeaseRecoveryFencesStaleWorker(t *testing.T) {
	j, now := journalFixture(t)
	ctx := context.Background()
	op, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Claim(ctx, op.ID, "other", time.Minute); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("Claim(live lease) = %v, want conflict", err)
	}
	if err := j.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteStep(ctx, claim, 0, "provider instance confirmed"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute)
	replacement, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.StartStep(ctx, claim, 1); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("StartStep(stale lease) = %v, want conflict", err)
	}
	stale := replacement
	stale.Generation++
	if err := j.StartStep(ctx, stale, 1); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("StartStep(wrong generation) = %v, want conflict", err)
	}
	got, err := j.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Status != "succeeded" {
		t.Error("reclaimed work lost completed step")
	}
	if err := j.Renew(ctx, replacement, time.Minute); err != nil {
		t.Fatal(err)
	}
}

// TestCompletionEvidenceAndReplay keeps success and ordered event evidence in one commit.
func TestCompletionEvidenceAndReplay(t *testing.T) {
	j, _ := journalFixture(t)
	ctx := context.Background()
	op, err := j.Create(ctx, provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := j.Claim(ctx, op.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteStep(ctx, claim, 1, ""); err == nil {
		t.Error("completion without active step/evidence accepted")
	}
	for index := range op.Steps {
		if err := j.StartStep(ctx, claim, index); err != nil {
			t.Fatal(err)
		}
		if err := j.CompleteStep(ctx, claim, index, "verified observation"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "succeeded" {
		t.Errorf("completed status = %s, want succeeded", got.Status)
	}
	events, err := j.Events(ctx, op.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, event := range events {
		if event.ID != int64(index+1) {
			t.Errorf("event ID = %d, want %d", event.ID, index+1)
		}
	}
	last := events[len(events)-1]
	if last.Operation.Status != "succeeded" || last.Evidence == "" {
		t.Error("success event lacks atomic completion evidence")
	}
	replay, err := j.Events(ctx, op.ID, last.ID-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].ID != last.ID {
		t.Error("Last-Event-ID replay returned the wrong suffix")
	}
}
