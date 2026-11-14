package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
)

// TestRetentionCandidates verifies that an outage does not reset inactivity,
// but uncertain inventory and newly active workspaces remain ineligible.
func TestRetentionCandidates(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"due", "disabled", "fresh", "uncertain", "active-again"} {
		var seconds *int64
		duration := int64(3600)
		if id != "disabled" {
			seconds = &duration
		}
		created := now.Add(-2 * time.Hour)
		if id == "fresh" {
			created = now.Add(-time.Minute)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
			generation,ownership_marker,provider_instance_id,created_at,updated_at,version,retention_seconds)
			VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
			'devcontainer.json',2,1024,40,'{"mode":"shared"}','stopped','stopped',1,'owned',$3,$4,$4,1,$5)`,
			id, testInstallation, id+"-vm", created.Format(time.RFC3339Nano), seconds); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM workspaces"); err != nil {
			t.Error(err)
		}
	})
	var inspected int
	inspect := func(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
		inspected++
		switch ref.WorkspaceID {
		case "uncertain":
			return provider.Inventory{}, provider.ErrOutcomeUnknown
		case "active-again":
			if _, err := db.ExecContext(ctx, "UPDATE workspaces SET last_activity_at=$1 WHERE id=$2", now.Format(time.RFC3339Nano), ref.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}
		return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, ObservedAt: now, Power: provider.Stopped,
			ArtifactID: "reservation-artifact", Release: "reservation-release", NetworkMode: "shared"}, nil
	}
	s := NewStore(db)
	limit := int64(7200)
	for _, policy := range []RetentionEligibility{
		{MaximumSeconds: nil, ReconciledAt: now.Add(-time.Hour), OutageGrace: 10 * time.Minute},
		{MaximumSeconds: &limit, OutageGrace: 10 * time.Minute},
		{MaximumSeconds: &limit, ReconciledAt: now.Add(-9 * time.Minute), OutageGrace: 10 * time.Minute},
	} {
		list, err := s.RetentionCandidates(ctx, testInstallation, now, policy, inspect)
		if err != nil || len(list) != 0 || inspected != 0 {
			t.Fatalf("ineligible policy returned %d candidates and %d inspections (%v), want zero", len(list), inspected, err)
		}
	}
	policy := RetentionEligibility{MaximumSeconds: &limit, ReconciledAt: now.Add(-10 * time.Minute), OutageGrace: 10 * time.Minute}
	list, err := s.RetentionCandidates(ctx, testInstallation, now, policy, inspect)
	if err != nil || len(list) != 1 || list[0].Ref.WorkspaceID != "due" || inspected != 3 {
		t.Fatalf("reconciled candidates = %+v and %d inspections (%v), want due only and 3 inspections", list, inspected, err)
	}
	if !list[0].Deadline.Equal(now.Add(-time.Hour)) {
		t.Errorf("deadline after grace = %v, want original elapsed deadline", list[0].Deadline)
	}
	if _, err := db.Exec("UPDATE workspaces SET retention_seconds=NULL WHERE id='due'"); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	if _, err := s.CommitRetentionDeletion(ctx, journal, list[0], now, policy); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("final policy recheck = %v, want no eligible workspace", err)
	}
	if _, err := db.Exec("UPDATE workspaces SET retention_seconds=3600 WHERE id='due'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER deny_retention_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitRetentionDeletion(ctx, journal, list[0], now, policy); err == nil {
		t.Fatal("failed audit accepted retention deletion")
	}
	var rolledBack bool
	if err := db.QueryRow(`SELECT desired_state='stopped' AND deletion_commit_at IS NULL
		AND NOT EXISTS(SELECT 1 FROM operations WHERE workspace_id='due') FROM workspaces WHERE id='due'`).Scan(&rolledBack); err != nil || !rolledBack {
		t.Fatalf("failed audit rolled back deletion and operation = %t (%v), want true", rolledBack, err)
	}
	if _, err := db.Exec("DROP TRIGGER deny_retention_audit"); err != nil {
		t.Fatal(err)
	}
	op, err := s.CommitRetentionDeletion(ctx, journal, list[0], now, policy)
	if err != nil || op.WorkspaceID == nil || *op.WorkspaceID != "due" || op.Type != "delete" ||
		op.OriginatingAction != "workspace.retention_delete" || op.Status != "pending" || len(op.Steps) != 3 {
		t.Fatalf("retention commitment = %+v (%v), want pending retention deletion with three steps", op, err)
	}
	if _, err := s.CommitRetentionDeletion(ctx, journal, list[0], now, policy); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second commitment = %v, want no eligible workspace", err)
	}
	var desired, observed, committed string
	var audits int
	if err := db.QueryRowContext(ctx, `SELECT desired_state,observed_state,deletion_commit_at,
		(SELECT count(*) FROM audit_events WHERE resource_id='due' AND action='workspace.retention_delete')
		FROM workspaces WHERE id='due'`).Scan(&desired, &observed, &committed, &audits); err != nil ||
		desired != "deleted" || observed != "stopped" || committed != now.Format(time.RFC3339Nano) || audits != 1 {
		t.Fatalf("commit state = %q/%q, time %q, %d audits (%v), want deleted/stopped, commit time and one audit", desired, observed, committed, audits, err)
	}
}

// TestRetentionWorkerPreservesDiskUntilVerified exercises the grace timeout,
// provider-enforced stop, partial removal, and a lost final deletion reply.
func TestRetentionWorkerPreservesDiskUntilVerified(t *testing.T) {
	t.Run("retention", func(t *testing.T) { verifyDeletionWorker(t, true) })
	t.Run("explicit", func(t *testing.T) { verifyDeletionWorker(t, false) })
}

func verifyDeletionWorker(t *testing.T, retention bool) {
	t.Helper()
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	const id = "retention-worker"
	machines := &retentionTestProvider{Provider: fake.New()}
	machine, err := machines.Create(ctx, provider.CreateRequest{
		Ref:         provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: id, Generation: 1, OwnershipMarker: "owned"},
		OperationID: "fixture-create", ArtifactID: "reservation-artifact", Release: "reservation-release", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machines.Start(ctx, provider.StartRequest{Ref: machine.Ref, OperationID: "fixture-start"}); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM reservations WHERE workspace_id=$1", "DELETE FROM workspaces WHERE id=$1"} {
			if _, err := db.Exec(query, id); err != nil {
				t.Error(err)
			}
		}
	})
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
		devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,
		ownership_marker,provider_instance_id,created_at,updated_at,version,retention_seconds)
		VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
		'devcontainer.json',2,1024,40,'{"mode":"nat"}','running','running',1,'owned',$3,$4,$4,1,600)`,
		id, testInstallation, machine.InstanceID, now.Add(-time.Hour).Format(time.RFC3339Nano))
	exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
		VALUES($1,2,1024,40,'reserved','reserved',$2)`, id, now.Add(-time.Hour).Format(time.RFC3339Nano))
	store := NewStore(db)
	limit := int64(3600)
	policy := RetentionEligibility{MaximumSeconds: &limit, ReconciledAt: now.Add(-time.Hour), OutageGrace: 10 * time.Minute}
	list, err := store.RetentionCandidates(ctx, testInstallation, now, policy, machines.Inspect)
	if err != nil || len(list) != 1 {
		t.Fatalf("retention candidates = %d (%v), want one", len(list), err)
	}
	journal := operation.NewJournal(db, clock, uuid.NewString)
	var op operation.Operation
	if retention {
		op, err = store.CommitRetentionDeletion(ctx, journal, list[0], now, policy)
	} else {
		err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var admitErr error
			op, admitErr = AdmitPower(ctx, tx, journal, PowerIntent{
				WorkspaceID: id, ActorID: "reservation-owner", RequestKey: "explicit-delete-fixture", Action: "delete", Version: 1,
			}, now)
			return admitErr
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(journal, nil, NewPowerExecutor(store, machines, clock, nil))
	run := func(want error) {
		t.Helper()
		if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, want) {
			t.Fatalf("retention worker = %v, want %v", err, want)
		}
	}
	run(errGuestShutdownPending)
	now = now.Add(59 * time.Second)
	run(errGuestShutdownPending)
	if machines.forced != 0 {
		t.Fatal("provider force stop ran during the graceful window")
	}
	now = now.Add(time.Second)
	run(nil)
	run(nil)
	wantForced := 0
	if retention {
		wantForced = 1
	}
	if machines.forced != wantForced {
		t.Fatalf("provider force stop calls = %d, want %d", machines.forced, wantForced)
	}
	checkReservation(t, db, id, "released", "reserved")
	machines.FailDelete()
	run(provider.ErrPartial)
	checkReservation(t, db, id, "released", "reserved")
	machines.LoseReply("delete")
	run(provider.ErrOutcomeUnknown)
	checkReservation(t, db, id, "released", "reserved")
	run(nil)
	checkReservation(t, db, id, "released", "released")
	finished, err := journal.Get(ctx, op.ID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("deletion status = %q (%v), want succeeded", finished.Status, err)
	}
}

type retentionTestProvider struct {
	*fake.Provider
	forced int
}

func (p *retentionTestProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	if req.ForceRetentionDelete {
		p.forced++
	}
	return p.Provider.Stop(ctx, req)
}
