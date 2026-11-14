package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestCapacityReservationLifecycle proves that admission and accounting
// survive retries, uncertain provider replies, restarts, and verified deletion.
func TestCapacityReservationLifecycle(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			db := openWorkspaceDatabase(t, profile)
			stamp := time.Unix(10000, 0).UTC()
			const first, second = "reservation-first", "reservation-second"
			seedReservationWorkspaces(t, db, first, second)
			const budgetCPU = 2
			store := NewStore(db)
			if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: budgetCPU, MemoryMiB: 1024, DiskMiB: 80}, 0, "admin", stamp); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec("DELETE FROM capacity_budgets WHERE installation_id=$1", testInstallation); err != nil {
					t.Errorf("remove reservation budget fixture: %v", err)
				}
			})
			reserve := func(id string) error {
				return database.Transaction(ctx, db, func(tx *sql.Tx) error {
					return ReserveStart(ctx, tx, testInstallation, id, 1, stamp)
				})
			}
			injected := errors.New("operation journal write failed")
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				if err := ReserveStart(ctx, tx, testInstallation, first, 1, stamp); err != nil {
					return err
				}
				return injected
			}); !errors.Is(err, injected) {
				t.Fatalf("aborted acceptance returned %v, want journal failure", err)
			}
			var held int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reservations").Scan(&held); err != nil || held != 0 {
				t.Fatalf("reservations after aborted acceptance = %d, %v, want zero", held, err)
			}
			for range 2 {
				if err := reserve(first); err != nil {
					t.Fatalf("reserve first workspace: %v", err)
				}
			}
			var capacity *CapacityError
			if err := reserve(second); !errors.As(err, &capacity) || capacity.Resource != "cpu" {
				t.Fatalf("reserve competing workspace = %v, want CPU refusal", err)
			}
			ref := provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: first, InstanceID: "vm-first", OwnershipMarker: "owned", Generation: 1}
			setDesired := func(state string) {
				t.Helper()
				if _, err := db.ExecContext(ctx, "UPDATE workspaces SET desired_state=$1 WHERE id=$2", state, first); err != nil {
					t.Fatal(err)
				}
			}
			setDesired("stopped")
			observe := func(inventory provider.Inventory) error {
				return database.Transaction(ctx, db, func(tx *sql.Tx) error {
					return ReleaseReservation(ctx, tx, inventory, stamp)
				})
			}
			if err := observe(provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Unknown, ObservedAt: stamp}); err == nil {
				t.Error("unknown power accepted as release proof")
			}
			if err := reserve(second); !errors.As(err, &capacity) {
				t.Fatalf("reserve after uncertain stop = %v, want refusal", err)
			}
			stopped := provider.Inventory{Ref: ref, Power: provider.Stopped, InstanceID: ref.InstanceID, ObservedAt: stamp}
			stale := stopped
			stale.Ref.Generation++
			if err := observe(stale); err == nil {
				t.Error("foreign generation accepted as release proof")
			}
			stale = stopped
			stale.ObservedAt = stamp.Add(-time.Second)
			if err := observe(stale); err == nil {
				t.Error("observation from before the reservation accepted as release proof")
			}
			if err := observe(stopped); err != nil {
				t.Fatal(err)
			}
			if err := reserve(second); err != nil {
				t.Fatal(err)
			}
			checkReservation(t, db, first, "released", "reserved")

			if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 4, MemoryMiB: 2048, DiskMiB: 80}, 1, "admin", stamp); err != nil {
				t.Fatal(err)
			}
			setDesired("running")
			if err := reserve(first); err != nil {
				t.Fatalf("restart with both disks already reserved: %v", err)
			}
			checkReservation(t, db, first, "reserved", "reserved")
			setDesired("deleted")
			deleted := stopped
			deleted.Power = provider.Deleted
			deleted.Resources = []provider.OwnedResource{{ID: "orphan", Type: "disk"}}
			if err := observe(deleted); err == nil {
				t.Error("partial delete accepted as release proof")
			}
			checkReservation(t, db, first, "reserved", "reserved")
			deleted.Resources = []provider.OwnedResource{{ID: "shared", Type: "disk", Shared: true}}
			if err := observe(deleted); err != nil {
				t.Fatal(err)
			}
			checkReservation(t, db, first, "released", "released")
			if err := reserve(first); err == nil {
				t.Error("deleted workspace allowed to reacquire capacity")
			}
			if _, err := db.ExecContext(ctx, "UPDATE workspaces SET desired_state='running' WHERE id=$1", first); err == nil {
				t.Error("schema allowed changing deleted desired state")
			}
			if _, err := db.ExecContext(ctx, "UPDATE workspaces SET cpu_count=4 WHERE id=$1", second); err == nil {
				t.Error("schema allowed changing pinned CPU allocation")
			}
		})
	}
}

// TestCapacityConcurrentStarts checks that two connections competing for one
// remaining slot leave exactly one reservation and one explicit refusal.
func TestCapacityConcurrentStarts(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			db := openWorkspaceDatabase(t, profile)
			db.SetMaxOpenConns(2)
			seedReservationWorkspaces(t, db, "concurrent-first", "concurrent-second")
			now := time.Now()
			store := NewStore(db)
			if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, "admin", now); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec("DELETE FROM capacity_budgets WHERE installation_id=$1", testInstallation); err != nil {
					t.Errorf("remove concurrent start budget fixture: %v", err)
				}
			})
			start := make(chan struct{})
			result := make(chan error, 2)
			for _, id := range []string{"concurrent-first", "concurrent-second"} {
				go func() {
					<-start
					result <- database.Transaction(ctx, db, func(tx *sql.Tx) error {
						return ReserveStart(ctx, tx, testInstallation, id, 1, now)
					})
				}()
			}
			close(start)
			accepted, refused := 0, 0
			for range 2 {
				err := <-result
				var capacity *CapacityError
				switch {
				case err == nil:
					accepted++
				case errors.As(err, &capacity):
					refused++
				default:
					t.Errorf("concurrent start returned unexpected error: %v", err)
				}
			}
			var held int
			if err := db.QueryRow("SELECT COUNT(*) FROM reservations").Scan(&held); err != nil || held != 1 || accepted != 1 || refused != 1 {
				t.Errorf("concurrent starts = %d accepted, %d refused, %d reservations, %v, want one of each", accepted, refused, held, err)
			}
		})
	}
}

func checkReservation(t *testing.T, db *sql.DB, workspace, wantCompute, wantDisk string) {
	t.Helper()
	var compute, disk string
	err := db.QueryRow("SELECT cpu_memory_state,disk_state FROM reservations WHERE workspace_id=$1", workspace).Scan(&compute, &disk)
	if err != nil || compute != wantCompute || disk != wantDisk {
		t.Fatalf("reservation = %s/%s, %v, want %s/%s", compute, disk, err, wantCompute, wantDisk)
	}
}

func seedReservationWorkspaces(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range ids {
			for _, query := range []string{"DELETE FROM reservations WHERE workspace_id=$1", "DELETE FROM workspaces WHERE id=$1"} {
				if _, err := db.Exec(query, id); err != nil {
					t.Errorf("remove reservation workspace fixture: %v", err)
				}
			}
		}
		for _, query := range []string{
			"DELETE FROM platform_artifacts WHERE id='reservation-artifact'",
			"DELETE FROM users WHERE id='reservation-owner'",
		} {
			if _, err := db.Exec(query); err != nil {
				t.Errorf("remove reservation provenance fixture: %v", err)
			}
		}
	})
	if _, err := db.Exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('reservation-owner','capacity@example.test','administrator','active','local','setup',$1,$1,1)`, testStamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO platform_artifacts
		(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,
		manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('reservation-artifact','reservation-release','amd64','test','test','1','[]','{}','{}','{}',
		$1,'test.utm','reservation-owner',$2,'available')`, "0000000000000000000000000000000000000000000000000000000000000000", testStamp); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		var instance any
		if i == 0 {
			instance = "vm-first"
		}
		if _, err := db.Exec(`INSERT INTO workspaces
			(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
			cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
			'.devcontainer/devcontainer.json',2,1024,40,'{}','running','unknown',1,'owned',$3,$4,$4,1)`,
			id, testInstallation, instance, testStamp); err != nil {
			t.Fatal(err)
		}
	}
}
