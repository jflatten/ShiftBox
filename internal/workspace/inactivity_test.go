package workspace

import (
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
)

// TestInactivityCandidates counts missing and stale polls without stopping
// fresh, unlimited, stopped, or busy workspaces, and rechecks activity at commit.
func TestInactivityCandidates(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			db := openWorkspaceDatabase(t, profile)
			ids := []string{"due", "fresh", "unlimited", "stopped", "busy", "boundary", "active-again"}
			now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
			seedInactivityWorkspaces(t, db, now.Add(-time.Hour), ids...)
			for _, id := range ids {
				inactivityExec(t, db, "UPDATE workspaces SET provider_instance_id=$2 WHERE id=$1", id, "vm-"+id)
			}
			inactivityExec(t, db, "UPDATE workspaces SET last_activity_at=$1 WHERE id='fresh'", now.Add(-time.Minute).Format(time.RFC3339Nano))
			inactivityExec(t, db, "UPDATE workspaces SET last_activity_at=$1 WHERE id='boundary'", now.Add(-30*time.Minute).Format(time.RFC3339Nano))
			inactivityExec(t, db, "UPDATE workspaces SET keep_alive_minutes=0 WHERE id='unlimited'")
			inactivityExec(t, db, "UPDATE workspaces SET desired_state='stopped',observed_state='stopped' WHERE id='stopped'")
			journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
			_, err := journal.Create(t.Context(), operation.Request{Actor: "reservation-owner", Key: "inactivity-busy-fixture",
				Type: "rebuild", OriginatingAction: "workspace.rebuild", WorkspaceID: &ids[4], Generation: 1,
				Steps: []operation.Step{{Key: "configure", Label: "Configure"}}})
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore(db)
			got, err := store.InactivityCandidates(t.Context(), testInstallation, now)
			if err != nil || !slices.Equal(got, []string{"active-again", "due"}) {
				t.Fatalf("InactivityCandidates = %v (%v), want active-again and due", got, err)
			}
			// A CPU/IDE activity update between discovery and commitment wins.
			inactivityExec(t, db, "UPDATE workspaces SET last_activity_at=$1 WHERE id='active-again'", now.Format(time.RFC3339Nano))
			if _, err := store.CommitInactivityStop(t.Context(), journal, "active-again", now); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("CommitInactivityStop(fresh activity) = %v, want no eligible workspace", err)
			}
			// A live policy change is applied before any shutdown is admitted.
			inactivityExec(t, db, "UPDATE installations SET keep_alive_default_minutes=0 WHERE id=$1", testInstallation)
			if _, err := store.CommitInactivityStop(t.Context(), journal, "due", now); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("CommitInactivityStop(unlimited default) = %v, want no eligible workspace", err)
			}
			inactivityExec(t, db, "UPDATE installations SET keep_alive_default_minutes=30 WHERE id=$1", testInstallation)
			op, err := store.CommitInactivityStop(t.Context(), journal, "due", now)
			if err != nil || op.Type != "stop" || op.OriginatingAction != "workspace.inactivity_stop" || op.Actor != "controller" || len(op.Steps) != 1 {
				t.Fatalf("CommitInactivityStop(due) = %+v (%v), want controller stop with one provider step", op, err)
			}
			if _, err := store.CommitInactivityStop(t.Context(), journal, "due", now); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("CommitInactivityStop(duplicate) = %v, want no eligible workspace", err)
			}
			var desired, observed string
			var audits int
			if err := db.QueryRow(`SELECT desired_state,observed_state,(SELECT count(*) FROM audit_events
				WHERE resource_id='due' AND action='workspace.inactivity_stop') FROM workspaces WHERE id='due'`).Scan(&desired, &observed, &audits); err != nil || desired != "stopped" || observed != "running" || audits != 1 {
				t.Errorf("inactivity commitment state = %s/%s, %d audits (%v), want stopped/running and one audit", desired, observed, audits, err)
			}
		})
	}
}

// TestInactivityStopWorker tolerates a missing guest and a lost provider reply,
// retaining disk and CPU reservations until the VM stop is confirmed.
func TestInactivityStopWorker(t *testing.T) {
	db := openWorkspaceDatabase(t, "sqlite")
	const id = "inactivity-worker"
	now := time.Now().UTC()
	seedInactivityWorkspaces(t, db, now.Add(-time.Hour), id)
	clock := time.Now
	machines := fake.New()
	machine, err := machines.Create(t.Context(), provider.CreateRequest{
		Ref:         provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: id, Generation: 1, OwnershipMarker: "owned"},
		OperationID: "fixture-create", ArtifactID: "reservation-artifact", Release: "reservation-release", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machines.Start(t.Context(), provider.StartRequest{Ref: machine.Ref, OperationID: "fixture-start"}); err != nil {
		t.Fatal(err)
	}
	inactivityExec(t, db, "UPDATE workspaces SET provider_instance_id=$2 WHERE id=$1", id, machine.InstanceID)
	inactivityExec(t, db, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
		VALUES($1,2,1024,40,'reserved','reserved',$2)`, id, now.Add(-time.Hour).Format(time.RFC3339Nano))
	store := NewStore(db)
	journal := operation.NewJournal(db, clock, uuid.NewString)
	op, err := store.CommitInactivityStop(t.Context(), journal, id, now)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(journal, nil, NewPowerExecutor(store, machines, clock, nil))
	machines.LoseReply("stop")
	if err := worker.RunOnce(t.Context(), op.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("inactivity stop lost reply = %v, want outcome unknown", err)
	}
	checkReservation(t, db, id, "reserved", "reserved")
	now = time.Now().UTC()
	if err := worker.RunOnce(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	checkReservation(t, db, id, "released", "reserved")
	got, err := journal.Get(t.Context(), op.ID)
	if err != nil || got.Status != "succeeded" {
		t.Errorf("inactivity stop operation = %s (%v), want succeeded", got.Status, err)
	}
}

// TestStartRestartsInactivityClock gives a restarted workspace a full keep-alive
// period even when its last activity predates a long stop.
func TestStartRestartsInactivityClock(t *testing.T) {
	db := openWorkspaceDatabase(t, "sqlite")
	const id = "inactivity-start"
	seedReservationWorkspaces(t, db, id)
	inactivityExec(t, db, "UPDATE workspaces SET desired_state='stopped',observed_state='stopped' WHERE id=$1", id)
	now := time.Now().UTC()
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	if err := NewStore(db).SetCapacityBudget(t.Context(), testInstallation, Allocation{}, 0, "reservation-owner", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM capacity_budgets WHERE installation_id=$1", testInstallation); err != nil {
			t.Error(err)
		}
	})
	err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		_, err := AdmitPower(t.Context(), tx, journal, PowerIntent{
			WorkspaceID: id, ActorID: "reservation-owner", RequestKey: "start-after-long-stop", Action: "start", Version: 1,
		}, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var stamp sql.NullString
	if err := db.QueryRow("SELECT last_activity_at FROM workspaces WHERE id=$1", id).Scan(&stamp); err != nil || stamp.String != now.Format(time.RFC3339Nano) {
		t.Errorf("start activity = %s (%v), want %s", stamp.String, err, now.Format(time.RFC3339Nano))
	}
}

func inactivityExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func seedInactivityWorkspaces(t *testing.T, db *sql.DB, created time.Time, ids ...string) {
	t.Helper()
	seedReservationWorkspaces(t, db)
	t.Cleanup(func() {
		for _, id := range ids {
			for _, query := range []string{"DELETE FROM reservations WHERE workspace_id=$1", "DELETE FROM workspaces WHERE id=$1"} {
				if _, err := db.Exec(query, id); err != nil {
					t.Error(err)
				}
			}
		}
	})
	for _, id := range ids {
		inactivityExec(t, db, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
			platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
			desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
			'devcontainer.json',2,1024,40,'{"mode":"nat"}','running','running',1,'owned',$3,$4,$4,1)`,
			id, testInstallation, "vm-"+id, created.UTC().Format(time.RFC3339Nano))
	}
}
