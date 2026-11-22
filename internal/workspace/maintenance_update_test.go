package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestMaintenancePowerRetainsDesiredState checks capacity and provider
// accounting for a management boot and a graceful return to stopped.
func TestMaintenancePowerRetainsDesiredState(t *testing.T) {
	db, journal, now := maintenanceFixture(t, "retained")
	ctx := t.Context()
	machines := &maintenanceStopProvider{Provider: fake.New()}
	ref := provider.WorkspaceRef{InstallationID: testInstallation, WorkspaceID: "retained", OwnershipMarker: "owned", Generation: 1}
	got, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create", ArtifactID: "reservation-artifact",
		Release: "reservation-release", NetworkMode: "nat", Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	maintenanceExec(t, db, `UPDATE workspaces SET desired_state='stopped', observed_state='stopped', provider_instance_id=$1,
		network_policy_override_json='{"mode":"nat"}'`, got.InstanceID)
	maintenanceExec(t, db, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		VALUES('retained',2,1024,40,'released','reserved',$1,$1)`, testStamp)
	deadline := now.Add(time.Hour)
	campaign, err := scheduleMaintenanceFixture(t, db, journal, "reservation-release", deadline, now)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 1, MemoryMiB: 1024, DiskMiB: 40}, 0, "reservation-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareMaintenanceTargets(ctx, journal, testInstallation, campaign.ID, deadline); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT operation_id FROM maintenance_targets WHERE campaign_id=$1`, campaign.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	available := false
	load := func(_ context.Context, product string) (release.GuestUpdate, error) {
		if !available {
			return release.GuestUpdate{}, release.ErrGuestUpdateUnavailable
		}
		return release.GuestUpdate{Metadata: release.GuestUpdateMetadata{Release: product, Protocol: "1"}}, nil
	}
	worker := store.MaintenanceUpdateWorker(journal, testInstallation, "1", load)
	executor := worker.Executor.(maintenanceUpdateExecutor)
	executor.power = &maintenancePower{machines: machines, now: time.Now}
	worker.Executor = executor
	if err := worker.RunOnce(ctx, id); !errors.Is(err, release.ErrGuestUpdateUnavailable) {
		t.Fatalf("management boot without a pinned release = %v, want release unavailable", err)
	}
	if inventory, err := machines.Inspect(ctx, got.Ref); err != nil || inventory.Power != provider.Stopped {
		t.Errorf("provider without pinned release = %s (%v), want stopped", inventory.Power, err)
	}
	checkReservation(t, db, "retained", "released", "reserved")
	available = true
	var waiting *operation.StepWaiting
	if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
		t.Fatalf("management boot without capacity = %v, want waiting", err)
	}
	checkReservation(t, db, "retained", "released", "reserved")
	if err := store.SetCapacityBudget(ctx, testInstallation, Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 1, "reservation-owner", now); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	checkReservation(t, db, "retained", "reserved", "reserved")
	assertPower := func(want string) {
		t.Helper()
		var desired, observed string
		if err := db.QueryRow(`SELECT desired_state,observed_state FROM workspaces WHERE id='retained'`).Scan(&desired, &observed); err != nil ||
			desired != "stopped" || observed != want {
			t.Errorf("maintenance workspace = %s/%s (%v), want stopped/%s", desired, observed, err, want)
		}
	}
	assertPower("running")
	if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
		t.Fatalf("staging without a receipt = %v, want waiting", err)
	}
	maintenanceExec(t, db, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status,problem)
		VALUES($1,1,'failed','guest.update_stage_failed')`, id)
	if err := worker.RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertPower("running")
	checkReservation(t, db, "retained", "reserved", "reserved")
	op, err := journal.Get(ctx, id)
	if err != nil || op.Status != "waiting" || op.Steps[1].Status != "failed" ||
		op.Steps[2].Status != "skipped" || op.Steps[3].Status != "skipped" {
		t.Fatalf("failed staging before restoration = %+v (%v), want durable failure with remaining update steps skipped", op, err)
	}
	// A newly constructed worker must finish the remaining power step.
	worker = store.MaintenanceUpdateWorker(journal, testInstallation, "1", nil)
	worker.Executor = executor
	if err := worker.RunOnce(ctx, id); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("uncertain maintenance stop = %v, want unknown outcome", err)
	}
	checkReservation(t, db, "retained", "reserved", "reserved")
	if err := worker.RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	assertPower("stopped")
	checkReservation(t, db, "retained", "released", "reserved")
	if machines.stopAttempts != 1 || machines.forcedStop {
		t.Errorf("maintenance stop attempts = %d, force = %t, want one graceful request", machines.stopAttempts, machines.forcedStop)
	}
	op, err = journal.Get(ctx, id)
	if err != nil || op.Status != "failed" || op.Steps[1].Detail == "" || op.Steps[4].Status != "succeeded" {
		t.Errorf("result after restoring power = %+v (%v), want original failure and successful restoration", op, err)
	}
}

type maintenanceStopProvider struct {
	provider.Provider
	stopAttempts int
	forcedStop   bool
}

func (p *maintenanceStopProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	p.stopAttempts++
	p.forcedStop = p.forcedStop || req.ForceRetentionDelete
	if _, err := p.Provider.Stop(ctx, req); err != nil {
		return provider.Inventory{}, err
	}
	return provider.Inventory{}, provider.ErrOutcomeUnknown
}

// TestMaintenanceUpdateWorker advances on durable matching receipts alone
// and blocks a rolled-back update even after the guest reports current.
func TestMaintenanceUpdateWorker(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "wrong_digest"} {
		t.Run(outcome, func(t *testing.T) {
			db, journal, now := maintenanceFixture(t, "retained")
			ctx := t.Context()
			maintenanceExec(t, db, `UPDATE workspaces SET observed_state='running',guest_release='test',protocol_version='1'`)
			deadline := now.Add(time.Hour)
			campaign, err := scheduleMaintenanceFixture(t, db, journal, "reservation-release", deadline, now)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore(db)
			if err := store.PrepareMaintenanceTargets(ctx, journal, testInstallation, campaign.ID, deadline); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := db.QueryRow(`SELECT operation_id FROM maintenance_targets WHERE campaign_id=$1`, campaign.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("pinned release"))
			load := func(_ context.Context, id string) (release.GuestUpdate, error) {
				if id != "test" {
					t.Errorf("loaded release %q, want test", id)
				}
				return release.GuestUpdate{Metadata: release.GuestUpdateMetadata{Release: id, Protocol: "1", Digest: digest}}, nil
			}
			worker := store.MaintenanceUpdateWorker(journal, testInstallation, "1", load)
			var waiting *operation.StepWaiting
			if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
				t.Fatalf("staging without a receipt = %v, want waiting", err)
			}
			reported := digest
			if outcome == "wrong_digest" {
				reported[0] ^= 1
			}
			maintenanceExec(t, db, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status,update_digest)
				VALUES($1,1,'succeeded',$2)`, id, hex.EncodeToString(reported[:]))
			if err := worker.RunOnce(ctx, id); err != nil {
				t.Fatal(err)
			}
			if outcome != "wrong_digest" {
				if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
					t.Fatalf("activation without a receipt = %v, want waiting", err)
				}
				maintenanceExec(t, db, `UPDATE guest_dispatches SET activation_status=$1 WHERE operation_id=$2`, outcome, id)
				if err := worker.RunOnce(ctx, id); err != nil {
					t.Fatal(err)
				}
				if outcome == "succeeded" {
					maintenanceExec(t, db, `UPDATE workspaces SET guest_release='previous',protocol_version='1' WHERE id='retained'`)
					if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
						t.Fatalf("verification with previous release = %v, want waiting", err)
					}
					maintenanceExec(t, db, `UPDATE workspaces SET guest_release='test',protocol_version='2' WHERE id='retained'`)
					if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
						t.Fatalf("verification with mismatched protocol = %v, want waiting", err)
					}
					maintenanceExec(t, db, `UPDATE workspaces SET protocol_version='1' WHERE id='retained'`)
					if err := worker.RunOnce(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := "failed"
			if outcome == "succeeded" {
				want = "succeeded"
			}
			if op, err := journal.Get(ctx, id); err != nil || op.Status != want {
				t.Errorf("update operation = %s (%v), want %s", op.Status, err, want)
			}
		})
	}
}
