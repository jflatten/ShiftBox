package workspace

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestScheduleMaintenanceCapturesRetainedWorkspaces verifies that scheduling
// records a warning without starting stopped guests or changing workspace state.
func TestScheduleMaintenanceCapturesRetainedWorkspaces(t *testing.T) {
	db, journal, now := maintenanceFixture(t, "stopped", "running", "deleted")
	maintenanceExec(t, db, `UPDATE workspaces SET desired_state='stopped',observed_state='stopped',generation=3 WHERE id='stopped'`)
	maintenanceExec(t, db, `UPDATE workspaces SET observed_state='running' WHERE id='running'`)
	maintenanceExec(t, db, `UPDATE workspaces SET desired_state='deleted',observed_state='deleted' WHERE id='deleted'`)
	maintenanceExec(t, db, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		VALUES('stopped',2,1024,40,'released','reserved',$1,$1)`, testStamp)

	deadline := now.Add(time.Hour)
	op, err := scheduleMaintenanceFixture(t, db, journal, "reservation-release", deadline, now)
	if err != nil {
		t.Fatal(err)
	}
	if op.Type != "update" || op.Status != "pending" || len(op.Steps) != 1 ||
		op.Steps[0].Key != "update_guests" || op.Steps[0].Status != "pending" {
		t.Fatalf("ScheduleMaintenance before deadline = %+v, want an update with just its pending campaign step", op)
	}

	rows, err := db.QueryContext(t.Context(), `SELECT workspace_id,generation,prior_desired_state,prior_observed_state,state,operation_id
		FROM maintenance_targets WHERE campaign_id=$1 ORDER BY workspace_id`, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type target struct {
		Workspace, Desired, Observed, State string
		Generation                          int64
		OperationID                         sql.NullString
	}
	var got []target
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.Workspace, &item.Generation, &item.Desired, &item.Observed, &item.State, &item.OperationID); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []target{
		{Workspace: "running", Desired: "running", Observed: "running", State: "pending", Generation: 1},
		{Workspace: "stopped", Desired: "stopped", Observed: "stopped", State: "pending", Generation: 3},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ScheduleMaintenance retained targets differ (-want +got):\n%s", diff)
	}

	var desired, observed, compute, disk string
	if err := db.QueryRowContext(t.Context(), `SELECT w.desired_state,w.observed_state,r.cpu_memory_state,r.disk_state
		FROM workspaces w JOIN reservations r ON r.workspace_id=w.id WHERE w.id='stopped'`).
		Scan(&desired, &observed, &compute, &disk); err != nil {
		t.Fatal(err)
	}
	if desired != "stopped" || observed != "stopped" || compute != "released" || disk != "reserved" {
		t.Errorf("ScheduleMaintenance stopped workspace = %s/%s with %s compute and %s disk, want stopped/stopped with released compute and reserved disk",
			desired, observed, compute, disk)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		view, err := ReadWorkspaceMaintenance(t.Context(), tx, "stopped", 3)
		if err != nil {
			return err
		}
		if view == nil || !view.Deadline.Equal(deadline) || view.ReleaseID != "reservation-release" ||
			view.State != "pending" || view.OperationID != nil {
			t.Errorf("ReadWorkspaceMaintenance(stopped, 3) = %+v, want pending warning for %s with no target operation", view, deadline)
		}
		view, err = ReadWorkspaceMaintenance(t.Context(), tx, "stopped", 2)
		if err == nil && view != nil {
			t.Errorf("ReadWorkspaceMaintenance(stopped, 2) = %+v, want no stale-generation warning", view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestScheduleMaintenanceRejectsUnavailableIntent verifies that a deadline
// must be in the future and the target artifact must match the active release.
func TestScheduleMaintenanceRejectsUnavailableIntent(t *testing.T) {
	db, journal, now := maintenanceFixture(t)
	maintenanceExec(t, db, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		SELECT 'incompatible-artifact','incompatible-release',architecture,platform_version,'incompatible-guest',protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state
		FROM platform_artifacts WHERE id='reservation-artifact'`)
	t.Cleanup(func() { maintenanceExec(t, db, "DELETE FROM platform_artifacts WHERE id='incompatible-artifact'") })
	for _, test := range []struct {
		name, release string
		deadline      time.Time
		want          error
	}{
		{name: "elapsed deadline", release: "reservation-release", deadline: now.Add(-time.Second), want: ErrMaintenanceInvalid},
		{name: "current deadline", release: "reservation-release", deadline: now, want: ErrMaintenanceInvalid},
		{name: "unavailable release", release: "missing", deadline: now.Add(time.Hour), want: ErrMaintenanceUnavailable},
		{name: "incompatible guest", release: "incompatible-release", deadline: now.Add(time.Hour), want: ErrMaintenanceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			op, err := scheduleMaintenanceFixture(t, db, journal, test.release, test.deadline, now)
			if !errors.Is(err, test.want) || op.ID != "" {
				t.Errorf("ScheduleMaintenance(%q, %v) = %q, %v, want no operation and %v", test.release, test.deadline, op.ID, err, test.want)
			}
		})
	}
	var campaigns, operations int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM maintenance_campaigns),
		(SELECT count(*) FROM operations WHERE originating_action='maintenance.manage')`).Scan(&campaigns, &operations); err != nil {
		t.Fatal(err)
	}
	if campaigns != 0 || operations != 0 {
		t.Errorf("ScheduleMaintenance invalid intent left %d campaigns and %d operations, want zero of both", campaigns, operations)
	}
}

// TestScheduleMaintenanceSkipsCommittedDeletion verifies that retention may
// finish an already committed deletion without acquiring maintenance work.
func TestScheduleMaintenanceSkipsCommittedDeletion(t *testing.T) {
	db, journal, now := maintenanceFixture(t, "deleting", "retained")
	maintenanceExec(t, db, `UPDATE workspaces SET desired_state='deleted',observed_state='stopped',deletion_commit_at=$1
		WHERE id='deleting'`, testStamp)
	op, err := scheduleMaintenanceFixture(t, db, journal, "reservation-release", now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	var captured bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM maintenance_targets
		WHERE campaign_id=$1 AND workspace_id='deleting')`, op.ID).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	if captured {
		t.Error("ScheduleMaintenance captured a workspace with a committed retention deletion, want exclusion")
	}
}

func maintenanceFixture(t *testing.T, ids ...string) (*sql.DB, *operation.Journal, time.Time) {
	t.Helper()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db, ids...)
	t.Cleanup(func() {
		maintenanceExec(t, db, "DELETE FROM maintenance_targets")
		maintenanceExec(t, db, "DELETE FROM maintenance_campaigns")
	})
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	return db, operation.NewJournal(db, func() time.Time { return now }, uuid.NewString), now
}

func scheduleMaintenanceFixture(t *testing.T, db *sql.DB, journal *operation.Journal, release string, deadline, now time.Time) (operation.Operation, error) {
	t.Helper()
	var op operation.Operation
	err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		var err error
		op, err = ScheduleMaintenance(t.Context(), tx, journal, testInstallation, "reservation-owner", "maintenance-domain-fixture", release, deadline, now)
		return err
	})
	return op, err
}

func maintenanceExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
