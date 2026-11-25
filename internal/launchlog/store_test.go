package launchlog_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	_ "modernc.org/sqlite"
)

func launchFixture(t *testing.T) (*sql.DB, *operation.Journal, operation.Operation, *time.Time) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "launch.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var id uint32
	journal := operation.NewJournal(db, func() time.Time { return now }, func() string { id++; return testsupport.ID(id) })
	workspaceID := testsupport.ID(40)
	op, err := journal.Create(ctx, operation.Request{
		OriginatingAction: "workspace.launch", Actor: "owner", Key: "launch-performance-fixture", Type: "provision",
		WorkspaceID: &workspaceID, Generation: 7,
		Steps: []operation.Step{{Key: "configure", Label: "Configure the development container"},
			{Key: "verify_ide", Label: "Verify browser IDE"}, {Key: "finish_setup", Label: "Finish setup"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, journal, op, &now
}

// TestReadLaunchPerformance measures readiness from admission and preserves it when a later hook fails.
func TestReadLaunchPerformance(t *testing.T) {
	db, journal, op, now := launchFixture(t)
	ctx := context.Background()
	claim, err := journal.Claim(ctx, op.ID, "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for step := range 3 {
		*now = now.Add(time.Second)
		if err := journal.StartStep(ctx, claim, step); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(2 * time.Second)
		if step < 2 {
			err = journal.CompleteStep(ctx, claim, step, "Verified")
		} else {
			err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
				return journal.FailStepInTx(ctx, tx, claim, step, "Required evidence was unavailable")
			})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil {
		t.Fatal(err)
	}
	if got.TimeToIDEReadyMS == nil || *got.TimeToIDEReadyMS != 6000 || got.TimeToSetupCompleteMS != nil {
		t.Fatalf("unexpected headline timings: %+v", got)
	}
	if len(got.Steps) != 3 || got.Steps[2].Status != "failed" || got.Steps[2].DurationMS != 2000 {
		t.Errorf("unexpected steps: %+v", got.Steps)
	}
}

// TestGuestSamplesAreScopedAndIdempotent covers retries and a stale or forged workspace identity.
func TestGuestSamplesAreScopedAndIdempotent(t *testing.T) {
	db, journal, op, now := launchFixture(t)
	ctx := context.Background()
	claim, err := journal.Claim(ctx, op.ID, "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,1,'pending')", op.ID); err != nil {
		t.Fatal(err)
	}
	sample := launchlog.GuestSample{ID: testsupport.ID(77), Phase: "ide_install", Outcome: "succeeded", DurationMS: 23000}
	for range 2 {
		if err := launchlog.RecordGuest(ctx, db, *op.WorkspaceID, op.Generation, op.ID, 1, sample, *now); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []struct {
		workspace            string
		generation, dispatch int64
	}{{"other-workspace", op.Generation, 1}, {*op.WorkspaceID, 6, 1}, {*op.WorkspaceID, 7, 2}} {
		err := launchlog.RecordGuest(ctx, db, scope.workspace, scope.generation, op.ID, scope.dispatch, sample, *now)
		if !errors.Is(err, launchlog.ErrGuestScope) {
			t.Errorf("scope = %+v, error = %v, want ErrGuestScope", scope, err)
		}
	}
	sample.Phase = "stdout: authorization=private"
	if err := launchlog.RecordGuest(ctx, db, *op.WorkspaceID, op.Generation, op.ID, 1, sample, *now); !errors.Is(err, launchlog.ErrInvalidSample) {
		t.Errorf("invalid payload error = %v, want ErrInvalidSample", err)
	}
	got, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Phase != "ide_install" || got.Events[0].DurationMS != 23000 {
		t.Errorf("events after retry and invalid reports: %+v", got.Events)
	}
}

// TestStartDiagnosticsAreIndependent verifies that starting a retained VM
// begins with an empty summary and captures its own scoped guest and cache data.
func TestStartDiagnosticsAreIndependent(t *testing.T) {
	db, journal, launch, now := launchFixture(t)
	ctx := t.Context()
	if err := launchlog.RecordCache(ctx, db, launch.ID, launchlog.CacheSample{
		StartedAt: *now, FinishedAt: *now, CachedBytes: 100, Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	start, err := journal.Create(ctx, operation.Request{
		OriginatingAction: "workspace.start", Actor: "owner", Key: "start-performance-fixture", Type: "start",
		WorkspaceID: launch.WorkspaceID, Generation: launch.Generation, Steps: launch.Steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := launchlog.Read(ctx, db, start.ID, *now)
	if err != nil || len(initial.Steps) != len(start.Steps) || len(initial.Events) != 0 || initial.Cache != nil {
		t.Errorf("Read(pending start) = %+v, %v, want fresh workflow steps and no samples", initial, err)
	}
	claim, err := journal.Claim(ctx, start.ID, "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return launchlog.MarkGuestAuthenticated(ctx, tx, *start.WorkspaceID, start.Generation, *now)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,1,'running')", start.ID); err != nil {
		t.Fatal(err)
	}
	if id, err := launchlog.ActiveCacheLaunch(ctx, db, *start.WorkspaceID, start.Generation); err != nil || id != start.ID {
		t.Errorf("ActiveCacheLaunch(running start) = %q, %v, want %q", id, err, start.ID)
	}
	sample := launchlog.GuestSample{ID: testsupport.ID(177), Phase: "ide_start", Outcome: "succeeded", DurationMS: 300}
	if err := launchlog.RecordGuest(ctx, db, *start.WorkspaceID, start.Generation, start.ID, 2, sample, *now); !errors.Is(err, launchlog.ErrGuestScope) {
		t.Errorf("RecordGuest(start with wrong dispatch version) = %v, want ErrGuestScope", err)
	}
	if err := launchlog.RecordGuest(ctx, db, *start.WorkspaceID, start.Generation, start.ID, 1, sample, *now); err != nil {
		t.Errorf("RecordGuest(start with current dispatch version) = %v, want success", err)
	}
	if err := launchlog.RecordCache(ctx, db, start.ID, launchlog.CacheSample{
		StartedAt: *now, FinishedAt: now.Add(time.Second), UpstreamBytes: 25, Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	latest, err := launchlog.Read(ctx, db, start.ID, *now)
	if err != nil || len(latest.Events) != 2 || latest.Events[0].Phase != "guest_authenticated" || latest.Events[1].ID != sample.ID ||
		latest.Cache == nil || latest.Cache.CachedBytes != 0 || latest.Cache.UpstreamBytes != 25 {
		t.Errorf("Read(start with samples) = %+v, %v, want only samples from the start", latest, err)
	}
	*now = now.Add(time.Minute)
	if _, err := journal.Create(ctx, operation.Request{
		OriginatingAction: "workspace.rebuild", Actor: "owner", Key: "rebuild-performance-fixture", Type: "rebuild",
		WorkspaceID: launch.WorkspaceID, Generation: launch.Generation, Steps: launch.Steps,
	}); err != nil {
		t.Fatal(err)
	}
	id, err := launchlog.WorkspaceOperation(ctx, db, *launch.WorkspaceID)
	if err != nil || id == nil {
		t.Fatalf("WorkspaceOperation(after rebuild) returned no operation, error = %v, want %q", err, start.ID)
	}
	if *id != start.ID {
		t.Errorf("WorkspaceOperation(after rebuild) = %q, want %q", *id, start.ID)
	}
}

// TestCacheSummaryOverlappingTransfers keeps the wall-clock window distinct from cumulative request time.
func TestCacheSummaryOverlappingTransfers(t *testing.T) {
	db, _, op, now := launchFixture(t)
	ctx := context.Background()
	for _, sample := range []launchlog.CacheSample{
		{StartedAt: *now, FinishedAt: now.Add(4 * time.Second), CachedBytes: 200, Status: 200},
		{StartedAt: now.Add(time.Second), FinishedAt: now.Add(3 * time.Second), UpstreamBytes: 100, Status: 200},
		{StartedAt: now.Add(2 * time.Second), FinishedAt: now.Add(3 * time.Second), Status: 429},
	} {
		if err := launchlog.RecordCache(ctx, db, op.ID, sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := launchlog.Read(ctx, db, op.ID, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cache == nil || got.Cache.Requests != 3 || got.Cache.CachedRequests != 1 || got.Cache.UpstreamBytes != 100 ||
		got.Cache.CachedBytes != 200 || got.Cache.RateLimitedRequests != 1 || got.Cache.WindowMS != 4000 {
		t.Errorf("cache summary = %+v", got.Cache)
	}
}

// TestPruneLaunchDiagnostics respects the default retention and preserves active workflow evidence.
func TestPruneLaunchDiagnostics(t *testing.T) {
	db, journal, op, now := launchFixture(t)
	ctx := context.Background()
	installation := testsupport.ID(200)
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Demo','sqlite','https://test','{workspace}.ide.test','{workspace}.preview.test','r','r','stamp','stamp',1)`, installation); err != nil {
		t.Fatal(err)
	}
	settings, err := launchlog.ReadRetention(ctx, db)
	if err != nil || settings.Days != 30 || settings.Version != 1 {
		t.Fatalf("retention = %+v, %v; want 30 days at version 1", settings, err)
	}
	if err := launchlog.RecordCache(ctx, db, op.ID, launchlog.CacheSample{StartedAt: *now, FinishedAt: *now, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if err := launchlog.RecordProvider(ctx, db, op.ID, "vm_create", *now, *now, nil); err != nil {
		t.Fatal(err)
	}
	if err := launchlog.Prune(ctx, db, now.AddDate(0, 0, 31)); err != nil {
		t.Fatal(err)
	}
	active, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil || active.Cache == nil || len(active.Events) != 1 {
		t.Fatalf("active diagnostics = %+v, %v", active, err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return journal.CancelInTx(ctx, tx, op.ID, "Launch cancelled", *now)
	}); err != nil {
		t.Fatal(err)
	}
	if err := launchlog.Prune(ctx, db, now.AddDate(0, 0, 30)); err != nil {
		t.Fatal(err)
	}
	atCutoff, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil || atCutoff.Cache == nil || len(atCutoff.Events) != 1 {
		t.Fatalf("diagnostics at cutoff = %+v, %v", atCutoff, err)
	}
	if err := launchlog.Prune(ctx, db, now.AddDate(0, 0, 31)); err != nil {
		t.Fatal(err)
	}
	expired, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil || expired.Cache != nil || len(expired.Events) != 0 {
		t.Fatalf("expired diagnostics = %+v, %v", expired, err)
	}
	if events, err := journal.Events(ctx, op.ID, 0); err != nil || len(events) < 2 {
		t.Fatalf("workflow journal after cleanup = %+v, %v", events, err)
	}
}

// TestProviderFailuresAreRedacted retains the category and elapsed time without a provider response payload.
func TestProviderFailuresAreRedacted(t *testing.T) {
	db, _, op, now := launchFixture(t)
	ctx := context.Background()
	if err := launchlog.RecordProvider(ctx, db, op.ID, "vm_create", *now, now.Add(time.Second),
		fmt.Errorf("private response with enrollment data: %w", provider.ErrOutcomeUnknown)); err != nil {
		t.Fatal(err)
	}
	got, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].ReasonCode != "outcome_unknown" || got.Events[0].Outcome != "failed" || got.Events[0].DurationMS != 1000 {
		t.Errorf("unexpected provider failure evidence: %+v", got.Events)
	}
}

// TestStepTimingWindow exposes the controller's actual start for assigning timed events to a step.
func TestStepTimingWindow(t *testing.T) {
	db, journal, op, now := launchFixture(t)
	ctx := t.Context()
	claim, err := journal.Claim(ctx, op.ID, "controller", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	start := now.Add(180*time.Millisecond + 700*time.Microsecond)
	*now = start
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	*now = start.Add(5410*time.Millisecond + 500*time.Microsecond)
	got, err := launchlog.Read(ctx, db, op.ID, *now)
	if err != nil {
		t.Fatal(err)
	}
	active := got.Steps[0]
	recordedStart, err := time.Parse(time.RFC3339Nano, active.StartedAt)
	if err != nil || !recordedStart.Equal(start) || active.DurationMS != 5410 {
		t.Errorf("active step starts %q with %d ms, want %s with 5410 ms", active.StartedAt, active.DurationMS, start)
	}
	if got.Steps[1].StartedAt != "" {
		t.Errorf("pending step has start %q", got.Steps[1].StartedAt)
	}
}
