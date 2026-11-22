//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestUTMCapacityRefusesExhaustedResources verifies that a real running VM
// keeps its resources when each independent budget refuses another launch.
func TestUTMCapacityRefusesExhaustedResources(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_CAPACITY") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_CAPACITY=1 for the native capacity refusal test")
	}
	runUTMGuestFixture(t, nil, nativeCapacity)
}

func verifyNativeCapacity(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider, vm provider.Inventory,
	owner, artifact string, allocation workspace.Allocation,
) {
	t.Helper()
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	store := workspace.NewStore(db)
	for _, test := range []struct {
		resource string
		used     int64
		budget   workspace.Allocation
	}{
		{"cpu", int64(allocation.CPU), workspace.Allocation{CPU: allocation.CPU, MemoryMiB: 2 * allocation.MemoryMiB, DiskMiB: 2 * allocation.DiskMiB}},
		{"memory", allocation.MemoryMiB, workspace.Allocation{CPU: 2 * allocation.CPU, MemoryMiB: allocation.MemoryMiB, DiskMiB: 2 * allocation.DiskMiB}},
		{"disk", allocation.DiskMiB, workspace.Allocation{CPU: 2 * allocation.CPU, MemoryMiB: 2 * allocation.MemoryMiB, DiskMiB: allocation.DiskMiB}},
	} {
		_, version, err := store.CapacityBudget(ctx, vm.Ref.InstallationID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetCapacityBudget(ctx, vm.Ref.InstallationID, test.budget, version, owner, time.Now()); err != nil {
			t.Fatalf("set independent %s capacity: %v", test.resource, err)
		}
		intent := workspace.LaunchIntent{
			InstallationID: vm.Ref.InstallationID, WorkspaceID: uuid.NewString(), OwnerID: owner,
			RequestKey: "native-refusal-" + test.resource, OwnershipMarker: uuid.NewString(),
			TemplateID: uuid.NewString(), TemplateRevision: 1, ArtifactID: artifact,
			RepositoryURL: "https://github.com/example/project", DevcontainerPath: ".devcontainer/devcontainer.json",
			NetworkPolicyJSON: `{"mode":"shared"}`, Allocation: allocation,
		}
		want := workspace.CapacityError{Resource: test.resource, Limit: test.used, Used: test.used, Requested: test.used}
		for range 2 {
			err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				_, err := workspace.AdmitLaunch(ctx, tx, journal, intent, time.Now())
				return err
			})
			got, ok := errors.AsType[*workspace.CapacityError](err)
			if !ok || *got != want {
				t.Fatalf("admit native launch under %s pressure = %v, want %+v", test.resource, err, want)
			}
		}
		var queued bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE request_key=$1)
			OR EXISTS(SELECT 1 FROM workspaces WHERE id=$2) OR EXISTS(SELECT 1 FROM reservations WHERE workspace_id=$2)`,
			intent.RequestKey, intent.WorkspaceID).Scan(&queued)
		if err != nil || queued {
			t.Fatalf("%s refusal created admission state: %t (%v)", test.resource, queued, err)
		}
		stillRunning, err := machines.Inspect(ctx, vm.Ref)
		if err != nil || stillRunning.Ref != vm.Ref || stillRunning.Power != provider.Running || stillRunning.Effective != vm.Effective {
			t.Fatalf("%s refusal changed the running VM: %+v (%v)", test.resource, stillRunning, err)
		}
		verifyOwnedDisk(t, vm, stillRunning)
		var compute, disk string
		err = db.QueryRowContext(ctx, `SELECT cpu_memory_state,disk_state FROM reservations WHERE workspace_id=$1`,
			vm.Ref.WorkspaceID).Scan(&compute, &disk)
		if err != nil || compute != "reserved" || disk != "reserved" {
			t.Fatalf("running VM reservation after %s refusal = %s/%s (%v), want reserved/reserved", test.resource, compute, disk, err)
		}
		t.Logf("%s pressure refused both requests without queueing or changing the running native VM", test.resource)
	}
}
