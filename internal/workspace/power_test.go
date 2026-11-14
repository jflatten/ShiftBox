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

// TestPowerWorkerReconcilesLostReplies checks that a restart observes the VM
// before retrying, releases only compute after stop, and waits for fresh guest
// contact and a working browser IDE after start.
func TestPowerWorkerReconcilesLostReplies(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	const id = "power-workspace"
	seedReservationWorkspaces(t, db)
	machines := &powerFixtureProvider{Provider: fake.New()}
	ref := provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: id, OwnershipMarker: "owned", Generation: 1}
	machine, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "create-fixture", ArtifactID: "reservation-artifact",
		Release: "reservation-release", NetworkMode: "nat", Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machines.Provider.Start(ctx, provider.StartRequest{Ref: machine.Ref, OperationID: "start-fixture"}); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM guest_registrations WHERE workspace_id=$1",
			"DELETE FROM reservations WHERE workspace_id=$1",
			"DELETE FROM workspaces WHERE id=$1",
		} {
			if _, err := db.Exec(query, id); err != nil {
				t.Error(err)
			}
		}
	})
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
		devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,
		ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
		'.devcontainer/devcontainer.json',2,1024,40,'{"mode":"nat"}','running','running',1,'owned',$3,$4,$4,1)`,
		id, testInstallation, machine.InstanceID, testStamp)
	exec(`INSERT INTO guest_registrations(workspace_id,generation,state,certificate_not_after,last_authenticated_at)
		VALUES($1,1,'active',$2,$3)`, id, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), testStamp)
	store := NewStore(db)
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, "reservation-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return ReserveStart(ctx, tx, testInstallation, id, 1, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	admit := func(action string) operation.Operation {
		t.Helper()
		var version int64
		if err := db.QueryRow("SELECT version FROM workspaces WHERE id=$1", id).Scan(&version); err != nil {
			t.Fatal(err)
		}
		var op operation.Operation
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			op, err = AdmitPower(ctx, tx, journal, PowerIntent{
				WorkspaceID: id, ActorID: "reservation-owner", Action: action, RequestKey: uuid.NewString(), Version: version,
			}, time.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	var ideAvailable bool
	probe := func(_ context.Context, workspace string, generation int64) error {
		if workspace != id || generation != 1 {
			t.Errorf("IDE probe scope = %s/%d, want %s/1", workspace, generation, id)
		}
		if !ideAvailable {
			return errors.New("tunnel unavailable")
		}
		return nil
	}
	run := func(op operation.Operation, want error) {
		t.Helper()
		worker := NewWorker(journal, NewLaunchExecutor(store, machines, nil, "", "", "", time.Now, probe),
			NewPowerExecutor(store, machines, time.Now, probe))
		if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, want) {
			t.Fatalf("run %s = %v, want %v", op.Type, err, want)
		}
	}
	checkState := func(op operation.Operation, observed, status string) {
		t.Helper()
		var state string
		if err := db.QueryRow("SELECT observed_state FROM workspaces WHERE id=$1", id).Scan(&state); err != nil || state != observed {
			t.Fatalf("observed state = %s, %v, want %s", state, err, observed)
		}
		if op, err := journal.Get(ctx, op.ID); err != nil || op.Status != status {
			t.Fatalf("operation status = %s, %v, want %s", op.Status, err, status)
		}
	}

	stop := admit("stop")
	run(stop, errGuestShutdownPending)
	if machines.stops != 0 {
		t.Fatal("provider stop ran before the guest shutdown step finished")
	}
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,1,'succeeded')", stop.ID)
	run(stop, nil)
	checkState(stop, "running", "waiting")
	machines.LoseReply("stop")
	run(stop, provider.ErrOutcomeUnknown)
	checkState(stop, "running", "waiting")
	checkReservation(t, db, id, "reserved", "reserved")
	run(stop, nil)
	checkState(stop, "stopped", "succeeded")
	checkReservation(t, db, id, "released", "reserved")

	start := admit("start")
	machines.LoseReply("start")
	run(start, provider.ErrOutcomeUnknown)
	run(start, errGuestContactPending)
	checkState(start, "running", "waiting")
	checkReservation(t, db, id, "reserved", "reserved")
	exec("UPDATE guest_registrations SET last_authenticated_at=$1 WHERE workspace_id=$2", time.Now().UTC().Format(time.RFC3339Nano), id)
	run(start, nil)
	checkState(start, "running", "waiting")
	run(start, errGuestConfigurationPending)
	checkState(start, "running", "waiting")
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'succeeded','container')", start.ID)
	run(start, nil)
	checkState(start, "running", "waiting")
	run(start, errIDEProbePending)
	checkState(start, "running", "waiting")
	ideAvailable = true
	run(start, nil)
	checkState(start, "running", "waiting")
	run(start, nil)
	checkState(start, "running", "succeeded")

	// Rebuild waits for new guest evidence and uses the existing VM and reservation.
	rebuild := admit("rebuild")
	run(rebuild, errGuestConfigurationPending)
	checkState(rebuild, "running", "waiting")
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'running','replacement')", rebuild.ID)
	run(rebuild, nil)
	ideAvailable = false
	run(rebuild, errIDEProbePending)
	ideAvailable = true
	run(rebuild, nil)
	run(rebuild, errGuestConfigurationPending)
	exec("UPDATE guest_dispatches SET status='succeeded' WHERE operation_id=$1", rebuild.ID)
	run(rebuild, nil)
	checkState(rebuild, "running", "succeeded")
	checkReservation(t, db, id, "reserved", "reserved")

	// Repair is available after failed development setup. Its worker waits
	// for a separate guest command and an authenticated repair IDE probe.
	exec("UPDATE operations SET status='failed',current_step='configure' WHERE id=$1", rebuild.ID)
	exec("UPDATE operation_steps SET status='failed' WHERE operation_id=$1 AND step_key='configure'", rebuild.ID)
	exec("UPDATE guest_registrations SET last_authenticated_at=$1 WHERE workspace_id=$2", time.Now().UTC().Format(time.RFC3339Nano), id)
	repair := admit("repair")
	run(repair, errGuestConfigurationPending)
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'succeeded','repair-container')", repair.ID)
	run(repair, nil)
	repairExecutor := NewPowerExecutor(store, machines, time.Now, probe)
	repairExecutor.probeRepair = func(_ context.Context, workspace string, generation int64) error {
		if workspace != id || generation != 1 {
			t.Errorf("repair probe scope = %s/%d, want %s/1", workspace, generation, id)
		}
		return nil
	}
	if err := NewWorker(journal, nil, repairExecutor).RunOnce(ctx, repair.ID); err != nil {
		t.Fatal(err)
	}
	checkState(repair, "running", "succeeded")
	checkReservation(t, db, id, "reserved", "reserved")

	// Exiting repair and retrying setup uses one operation and fresh IDE evidence.
	retry := admit("retry")
	run(retry, errGuestConfigurationPending)
	exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id) VALUES($1,1,'running','retried')", retry.ID)
	run(retry, nil)
	run(retry, nil)
	run(retry, errGuestConfigurationPending)
	exec("UPDATE guest_dispatches SET status='succeeded' WHERE operation_id=$1", retry.ID)
	run(retry, nil)
	checkState(retry, "running", "succeeded")
	if machines.starts != 1 || machines.stops != 1 {
		t.Fatalf("provider calls = %d starts and %d stops, want one per operation", machines.starts, machines.stops)
	}

	// Offboarding revokes guest access before requesting a provider stop.
	// The worker must proceed using the committed stop intent without a poll.
	exec("UPDATE users SET membership_state='disabled' WHERE id='reservation-owner'")
	exec("UPDATE workspaces SET desired_state='stopped',provider_instance_id=NULL WHERE id=$1", id)
	workspaceID := id
	offboard, err := journal.Create(ctx, operation.Request{
		Actor: "reservation-owner", Key: uuid.NewString(), OriginatingAction: "user.disable",
		Type: "stop", WorkspaceID: &workspaceID, Generation: 1,
		Steps: []operation.Step{{Key: "stop", Label: "Stop workspace"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	machines.LoseReply("stop")
	run(offboard, provider.ErrOutcomeUnknown)
	checkReservation(t, db, id, "reserved", "reserved")
	waiting, err := journal.Get(ctx, offboard.ID)
	if err != nil {
		t.Fatal(err)
	}
	const wantDetail = "The provider has not confirmed the workspace stopped. Check that UTM is running and the VM is reachable."
	if waiting.Status != "waiting" || waiting.Steps[0].Detail != wantDetail {
		t.Errorf("offboarding after lost response = %s, %q, want waiting with recovery guidance", waiting.Status, waiting.Steps[0].Detail)
	}
	run(offboard, nil)
	checkState(offboard, "stopped", "succeeded")
	checkReservation(t, db, id, "released", "reserved")
}

// TestPowerRequestUsesEditedNetworkPolicy verifies a stopped workspace's
// current policy takes precedence over its immutable launch mode on start.
func TestPowerRequestUsesEditedNetworkPolicy(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	const id = "power-policy-workspace"
	t.Cleanup(func() {
		for _, query := range []string{"DELETE FROM reservations WHERE workspace_id=$1", "DELETE FROM workspaces WHERE id=$1"} {
			if _, err := db.Exec(query, id); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
		platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		network_policy_override_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
		'.devcontainer/devcontainer.json',2,1024,40,'{"mode":"nat"}','{"mode":"shared"}','stopped','stopped',1,'owned','instance',$3,$3,1)`,
		id, testInstallation, testStamp); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, "reservation-owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	var op operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		op, err = AdmitPower(ctx, tx, journal, PowerIntent{
			WorkspaceID: id, ActorID: "reservation-owner", Action: "start", RequestKey: uuid.NewString(), Version: 1,
		}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Claim(ctx, op.ID, "fixture", time.Minute); err != nil {
		t.Fatal(err)
	}
	op, err := journal.Get(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := op.Steps[0]
	step.Status = "active"
	req, err := NewPowerExecutor(store, nil, time.Now, nil).request(ctx, op, step)
	if err != nil || req.NetworkMode != "shared" {
		t.Fatalf("start request network = %q (%v), want shared", req.NetworkMode, err)
	}
}

type powerFixtureProvider struct {
	*fake.Provider
	starts, stops int
}

func (p *powerFixtureProvider) Start(ctx context.Context, request provider.StartRequest) (provider.Inventory, error) {
	p.starts++
	return p.Provider.Start(ctx, request)
}

func (p *powerFixtureProvider) Stop(ctx context.Context, request provider.StopRequest) (provider.Inventory, error) {
	p.stops++
	if request.ForceRetentionDelete {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	return p.Provider.Stop(ctx, request)
}
