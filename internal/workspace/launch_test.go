package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestLaunchAdmission commits the launch snapshot, reservation, and operation
// at once, replays the original operation, and rolls back a refused launch.
func TestLaunchAdmission(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			db := openWorkspaceDatabase(t, profile)
			seedReservationWorkspaces(t, db)
			defer func() {
				for _, stmt := range []string{
					"DELETE FROM operation_events WHERE operation_id='test-launch-op'",
					"DELETE FROM operation_steps WHERE operation_id='test-launch-op'",
					"DELETE FROM operations WHERE id='test-launch-op'",
					"DELETE FROM reservations WHERE workspace_id IN ('launch-one','launch-two')",
					"DELETE FROM workspaces WHERE id IN ('launch-one','launch-two')",
					"DELETE FROM capacity_budgets WHERE installation_id=$1",
				} {
					var args []any
					if stmt == "DELETE FROM capacity_budgets WHERE installation_id=$1" {
						args = append(args, testInstallation)
					}
					if _, err := db.Exec(stmt, args...); err != nil {
						t.Errorf("clean up launch fixture: %v", err)
					}
				}
			}()
			now := time.Now().UTC()
			store := NewStore(db)
			allocation := Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
			if err := store.SetCapacityBudget(ctx, testInstallation, allocation, 0, "reservation-owner", now); err != nil {
				t.Fatal(err)
			}
			sequence := 0
			journal := operation.NewJournal(db, func() time.Time { return now }, func() string {
				sequence++
				if sequence == 1 {
					return "test-launch-op"
				}
				return "refused-launch-op"
			})
			launch := LaunchIntent{
				InstallationID: testInstallation, WorkspaceID: "launch-one", OwnerID: "reservation-owner",
				RequestKey: "launch-request-one", TemplateID: "template", TemplateRevision: 1, ArtifactID: "reservation-artifact",
				RepositoryURL: "https://github.com/example/project", DevcontainerPath: ".devcontainer/devcontainer.json",
				Allocation: allocation, NetworkPolicyJSON: `{"mode":"shared"}`, SecretNames: []string{"API_KEY"}, OwnershipMarker: "owned-one",
			}
			initialRetention := int64(3600)
			launch.RetentionSeconds = &initialRetention
			admit := func(intent LaunchIntent) (operation.Operation, error) {
				var result operation.Operation
				err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
					var err error
					result, err = AdmitLaunch(ctx, tx, journal, intent, now)
					return err
				})
				return result, err
			}
			first, err := admit(launch)
			if err != nil || first.WorkspaceID == nil || *first.WorkspaceID != launch.WorkspaceID || first.Status != "pending" || len(first.Steps) < 4 {
				t.Fatalf("AdmitLaunch = %+v, %v, want pending operation with persisted workflow", first, err)
			}
			checkReservation(t, db, launch.WorkspaceID, "reserved", "reserved")
			var desired, observed, names string
			var deadline sql.NullString
			err = db.QueryRow("SELECT desired_state,observed_state,secret_names_json,retention_deadline FROM workspaces WHERE id=$1", launch.WorkspaceID).
				Scan(&desired, &observed, &names, &deadline)
			if err != nil || desired != "running" || observed != "unknown" || names != `["API_KEY"]` ||
				deadline.String != now.Add(time.Hour).Format(time.RFC3339Nano) {
				t.Errorf("launch snapshot = %s/%s %s, deadline %v, %v, want running/unknown, captured names and deadline one hour from launch", desired, observed, names, deadline, err)
			}

			launch.WorkspaceID = "launch-two"
			launch.OwnershipMarker = "owned-two"
			if _, err := db.ExecContext(ctx, "UPDATE workspaces SET retention_seconds=7200 WHERE id=$1", "launch-one"); err != nil {
				t.Fatal(err)
			}
			replay, err := admit(launch)
			if err != nil || replay.ID != first.ID || sequence != 1 {
				t.Fatalf("replayed launch = %s, %v, counter %d, want original operation", replay.ID, err, sequence)
			}
			launch.TemplateRevision++
			if _, err := admit(launch); !errors.Is(err, ErrLaunchConflict) {
				t.Errorf("reused key with revised template = %v, want conflict", err)
			}
			launch.RequestKey = "launch-request-two"
			var capacity *CapacityError
			if _, err := admit(launch); !errors.As(err, &capacity) {
				t.Errorf("over-capacity admission = %v, want capacity error", err)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE id='launch-two'").Scan(&count); err != nil || count != 0 {
				t.Errorf("refused workspace count = %d, %v, want zero", count, err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM operations WHERE requested_by=$1", launch.OwnerID).Scan(&count); err != nil || count != 1 {
				t.Errorf("operation count = %d, %v, want one", count, err)
			}
		})
	}
}

// TestLaunchSnapshotRejectsUnverifiedInput keeps structurally invalid snapshots
// out of admission before they can create pending work.
func TestLaunchSnapshotRejectsUnverifiedInput(t *testing.T) {
	_, err := AdmitLaunch(context.Background(), nil, nil, LaunchIntent{}, time.Now())
	if !errors.Is(err, provider.ErrInvalidState) {
		t.Errorf("empty launch = %v, want invalid state", err)
	}
}
