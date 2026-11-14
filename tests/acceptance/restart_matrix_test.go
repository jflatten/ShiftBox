package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestRestartResumesMaintenancePower stops the worker after the provider
// changes power but before the controller can record the result.
func TestRestartResumesMaintenancePower(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	clock := func() time.Time { return now }
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "maintenance-power.db")}
	open := func() *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	machines := &interruptedMaintenanceProvider{Provider: fake.New(), now: clock}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, OwnershipMarker: "fixture-owner"}
	created, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create",
		ArtifactID: "fixture-image", Release: "fixture", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	ref, actor := created.Ref, uuid.NewString()
	seedRecoveryRestart(t, db, ref, actor, now)
	exec("UPDATE workspaces SET observed_state='stopped' WHERE id=$1", ref.WorkspaceID)
	exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		VALUES($1,2,1024,40,'released','reserved',$2,$2)`, ref.WorkspaceID, now.Format(time.RFC3339Nano))
	exec(`INSERT INTO guest_registrations(workspace_id,generation,state,certificate_not_after,last_authenticated_at)
		VALUES($1,$2,'active',$3,$4)`, ref.WorkspaceID, ref.Generation,
		now.Add(24*time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	journal := operation.NewJournal(db, clock, uuid.NewString)
	store := workspace.NewStore(db)
	if err := store.SetCapacityBudget(ctx, ref.InstallationID, workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}, 0, actor, now); err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Hour)
	var campaign operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		campaign, err = workspace.ScheduleMaintenance(ctx, tx, journal, ref.InstallationID, actor,
			"maintenance-power-restart", "fixture", deadline, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now = deadline
	if err := store.PrepareMaintenanceTargets(ctx, journal, ref.InstallationID, campaign.ID, now); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := db.QueryRowContext(ctx, "SELECT operation_id FROM maintenance_targets WHERE campaign_id=$1", campaign.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	load := func(context.Context, string) (release.GuestUpdate, error) {
		return release.GuestUpdate{Metadata: release.GuestUpdateMetadata{Release: "fixture", Protocol: "1"}}, nil
	}
	newWorker := func() *operation.Worker {
		// The guest already has a valid certificate. Reconciliation only needs
		// enrollment to clean up delivery records after fresh guest contact.
		launch := workspace.NewLaunchExecutor(store, machines, guest.NewEnrollment(db, nil), "", "", "", clock, nil)
		_, worker := store.MaintenanceWorkers(journal, ref.InstallationID, "1", load, launch)
		return worker
	}
	worker := newWorker()
	interrupt := func(step string, power provider.PowerState) {
		t.Helper()
		interrupted, cancel := context.WithCancel(ctx)
		defer cancel()
		machines.interrupt = cancel
		if err := worker.RunOnce(interrupted, target); !errors.Is(err, context.Canceled) {
			t.Fatalf("RunOnce(%s) after interrupt = %v, want context canceled", step, err)
		}
		inventory, err := machines.Inspect(ctx, ref)
		if err != nil || inventory.Power != power {
			t.Fatalf("Inspect after %s = %s (%v), want %s", step, inventory.Power, err, power)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		journal = operation.NewJournal(db, clock, uuid.NewString)
		store = workspace.NewStore(db)
		lease := worker.LeaseDuration
		worker = newWorker()
		if err := worker.RunOnce(ctx, target); !errors.Is(err, operation.ErrConflict) {
			t.Fatalf("RunOnce(%s) before lease expiry = %v, want conflict", step, err)
		}
		now = now.Add(lease + time.Second)
	}
	assertStates := func(wantObserved, wantCPU string) {
		t.Helper()
		var desired, observed, cpu, disk string
		err := db.QueryRowContext(ctx, `SELECT w.desired_state,w.observed_state,r.cpu_memory_state,r.disk_state
			FROM workspaces w JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`,
			ref.WorkspaceID).Scan(&desired, &observed, &cpu, &disk)
		if err != nil || desired != "stopped" || observed != wantObserved || cpu != wantCPU || disk != "reserved" {
			t.Fatalf("workspace and reservation = %s/%s %s/%s (%v), want stopped/%s %s/reserved",
				desired, observed, cpu, disk, err, wantObserved, wantCPU)
		}
	}
	interrupt("maintenance_boot", provider.Running)
	assertStates("stopped", "reserved")
	err = worker.RunOnce(ctx, target)
	if _, ok := errors.AsType[*operation.StepWaiting](err); !ok {
		t.Fatalf("RunOnce(maintenance_boot) with stale guest contact = %v, want waiting", err)
	}
	exec("UPDATE guest_registrations SET last_authenticated_at=$1 WHERE workspace_id=$2", now.Format(time.RFC3339Nano), ref.WorkspaceID)
	if err := worker.RunOnce(ctx, target); err != nil {
		t.Fatal(err)
	}
	assertStates("running", "reserved")
	exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,problem)
		VALUES($1,1,'failed','guest.update_stage_failed')`, target)
	if err := worker.RunOnce(ctx, target); err != nil {
		t.Fatal(err)
	}
	interrupt("maintenance_restore", provider.Stopped)
	assertStates("running", "reserved")
	if err := worker.RunOnce(ctx, target); err != nil {
		t.Fatal(err)
	}
	assertStates("stopped", "released")
	op, err := journal.Get(ctx, target)
	if err != nil || op.Status != "failed" || op.Steps[0].Status != "succeeded" ||
		op.Steps[1].Status != "failed" || op.Steps[2].Status != "skipped" ||
		op.Steps[3].Status != "skipped" || op.Steps[4].Status != "succeeded" {
		t.Fatalf("target after restart = %+v (%v), want successful boot and restore with staging failure", op, err)
	}
	if machines.starts != 1 || machines.stops != 1 || machines.forcedStop {
		t.Errorf("provider requests = %d starts, %d stops, force=%t, want one start and one graceful stop",
			machines.starts, machines.stops, machines.forcedStop)
	}
}

type interruptedMaintenanceProvider struct {
	provider.Provider
	now           func() time.Time
	interrupt     context.CancelFunc
	starts, stops int
	forcedStop    bool
}

func (p *interruptedMaintenanceProvider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	inventory, err := p.Provider.Inspect(ctx, ref)
	inventory.ObservedAt = p.now()
	return inventory, err
}

func (p *interruptedMaintenanceProvider) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	p.starts++
	inventory, err := p.Provider.Start(ctx, req)
	if err == nil && p.interrupt != nil {
		p.interrupt()
		p.interrupt = nil
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return inventory, err
}

func (p *interruptedMaintenanceProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	p.stops++
	p.forcedStop = p.forcedStop || req.ForceRetentionDelete
	inventory, err := p.Provider.Stop(ctx, req)
	if err == nil && p.interrupt != nil {
		p.interrupt()
		p.interrupt = nil
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return inventory, err
}

// TestRestartResumesMaintenanceReceipts interrupts the controller after each
// fixture receipt is persisted and before the worker completes its step.
func TestRestartResumesMaintenanceReceipts(t *testing.T) {
	for _, activation := range []string{"succeeded", "failed"} {
		t.Run(activation, func(t *testing.T) {
			ctx, now := t.Context(), time.Now().UTC()
			cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "maintenance.db")}
			open := func() *sql.DB {
				t.Helper()
				db, err := database.Open(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			db := open()
			if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			clock := func() time.Time { return now }
			journal := operation.NewJournal(db, clock, uuid.NewString)
			ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
				Generation: 1, InstanceID: "retained-instance", OwnershipMarker: "fixture-owner"}
			actor := uuid.NewString()
			seedRecoveryRestart(t, db, ref, actor, now)
			exec("UPDATE workspaces SET desired_state='running' WHERE id=$1", ref.WorkspaceID)
			deadline := now.Add(time.Hour)
			var campaign operation.Operation
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				var err error
				campaign, err = workspace.ScheduleMaintenance(ctx, tx, journal, ref.InstallationID, actor,
					"maintenance-receipt-restart", "fixture", deadline, now)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			now = deadline
			store := workspace.NewStore(db)
			if err := store.PrepareMaintenanceTargets(ctx, journal, ref.InstallationID, campaign.ID, now); err != nil {
				t.Fatal(err)
			}
			var target string
			if err := db.QueryRowContext(ctx, "SELECT operation_id FROM maintenance_targets WHERE campaign_id=$1", campaign.ID).Scan(&target); err != nil {
				t.Fatal(err)
			}
			metadata := release.GuestUpdateMetadata{Release: "fixture", Protocol: "1", Digest: sha256.Sum256([]byte("pinned restart fixture"))}
			load := func(context.Context, string) (release.GuestUpdate, error) {
				return release.GuestUpdate{Metadata: metadata}, nil
			}
			interrupt := func(index int, persistReceipt func()) {
				t.Helper()
				const lease = time.Minute
				claim, err := journal.Claim(ctx, target, "interrupted-controller", lease)
				if err != nil {
					t.Fatal(err)
				}
				if err := journal.StartStep(ctx, claim, index); err != nil {
					t.Fatal(err)
				}
				persistReceipt()
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				now = now.Add(lease + time.Second)
				db = open()
				journal = operation.NewJournal(db, clock, uuid.NewString)
				store = workspace.NewStore(db)
				if err := store.MaintenanceUpdateWorker(journal, ref.InstallationID, "1", load).RunOnce(ctx, target); err != nil {
					t.Fatal(err)
				}
			}
			interrupt(0, func() {
				exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,update_digest)
					VALUES($1,1,'succeeded',$2)`, target, hex.EncodeToString(metadata.Digest[:]))
			})
			op, err := journal.Get(ctx, target)
			if err != nil || op.Steps[0].Status != "succeeded" || op.Steps[1].Status != "pending" {
				t.Fatalf("after staging restart = %+v (%v), want staging complete and activation pending", op, err)
			}
			interrupt(1, func() {
				exec("UPDATE guest_dispatches SET activation_status=$1 WHERE operation_id=$2", activation, target)
				if activation == "succeeded" {
					exec("UPDATE workspaces SET guest_release='fixture',protocol_version='1' WHERE id=$1", ref.WorkspaceID)
				}
			})
			if activation == "succeeded" {
				if err := store.MaintenanceUpdateWorker(journal, ref.InstallationID, "1", load).RunOnce(ctx, target); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.MaintenanceCampaignWorker(journal, ref.InstallationID, clock).RunOnce(ctx, campaign.ID); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{target, campaign.ID} {
				op, err := journal.Get(ctx, id)
				if err != nil || op.Status != activation {
					t.Errorf("operation %s after activation restart = %s (%v), want %s", id, op.Status, err, activation)
				}
			}
		})
	}
}

// TestRestartPreservesMaintenanceSchedule reopens the controller database
// during the warning period, after the deadline, and after preparing work.
// Replay must preserve the same target operation and reserved disk.
func TestRestartPreservesMaintenanceSchedule(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	deadline := now.Add(time.Hour)
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "maintenance.db")}
	open := func() *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, InstanceID: "retained-instance", OwnershipMarker: "fixture-owner"}
	actor := uuid.NewString()
	seedRecoveryRestart(t, db, ref, actor, now)
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET observed_state='stopped' WHERE id=$1", ref.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,
		disk_state,reserved_at,cpu_memory_released_at) VALUES($1,2,1024,40,'released','reserved',$2,$2)`,
		ref.WorkspaceID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	schedule := func(db *sql.DB) operation.Operation {
		t.Helper()
		journal := operation.NewJournal(db, clock, uuid.NewString)
		var op operation.Operation
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			op, err = workspace.ScheduleMaintenance(ctx, tx, journal, ref.InstallationID, actor,
				"restart-scheduled-maintenance", "fixture", deadline, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return op
	}
	first := schedule(db)
	var targetOperation string
	for _, elapsed := range []time.Duration{30 * time.Minute, 31 * time.Minute, time.Second} {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		now = now.Add(elapsed)
		db = open()
		replay := schedule(db)
		if replay.ID != first.ID || replay.Status != "pending" {
			t.Fatalf("scheduled replay = %s/%s, want %s/pending", replay.ID, replay.Status, first.ID)
		}
		journal := operation.NewJournal(db, clock, uuid.NewString)
		if err := workspace.NewStore(db).PrepareMaintenanceTargets(ctx, journal, ref.InstallationID, first.ID, now); err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			warning, err := workspace.ReadWorkspaceMaintenance(ctx, tx, ref.WorkspaceID, ref.Generation)
			if err != nil || warning == nil || !warning.Deadline.Equal(deadline) {
				t.Fatalf("workspace warning after restart = %+v (%v), want original deadline", warning, err)
			}
			if now.Before(deadline) {
				if warning.OperationID != nil || warning.State != "pending" {
					t.Fatalf("workspace before deadline = %+v, want pending without a target operation", warning)
				}
				return nil
			}
			if warning.OperationID == nil || warning.State != "waiting_safe" {
				t.Fatalf("workspace after deadline = %+v, want a durable operation waiting to start", warning)
			}
			if targetOperation != "" && *warning.OperationID != targetOperation {
				t.Errorf("maintenance target after restart = %s, want %s", *warning.OperationID, targetOperation)
			}
			targetOperation = *warning.OperationID
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var count int
		var desired, observed, compute, disk string
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM maintenance_targets WHERE campaign_id=$1),
			w.desired_state,w.observed_state,r.cpu_memory_state,r.disk_state
			FROM workspaces w JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$2`,
			first.ID, ref.WorkspaceID).Scan(&count, &desired, &observed, &compute, &disk)
		if err != nil || count != 1 || desired != "stopped" || observed != "stopped" || compute != "released" || disk != "reserved" {
			t.Errorf("schedule after restart = %d targets, %s/%s workspace, %s/%s resources (%v), want one stopped target with retained disk",
				count, desired, observed, compute, disk, err)
		}
	}
}

// TestRestartInterruptsRestoreValidation verifies that startup interrupts
// pending and active validation jobs whose private inputs died with the process.
func TestRestartInterruptsRestoreValidation(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "pending"
		if active {
			name = "active"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "restore.db")}
			open := func() *sql.DB {
				t.Helper()
				db, err := database.Open(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			db := open()
			if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
				t.Fatal(err)
			}
			journal := operation.NewJournal(db, time.Now, uuid.NewString)
			op, err := journal.Create(ctx, operation.Request{
				Type: "restore", Actor: "fixture-admin", Key: "restore-validation-restart",
				Generation: 1, OriginatingAction: "restore.validate",
				Steps: []operation.Step{{Key: "validate", Label: "Verify archive and replacement"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if active {
				claim, err := journal.Claim(ctx, op.ID, "restore-validation", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if err := journal.StartStep(ctx, claim, 0); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = open()
			journal = operation.NewJournal(db, time.Now, uuid.NewString)
			for range 2 {
				if err := web.InterruptRestoreValidations(ctx, db, journal); err != nil {
					t.Fatal(err)
				}
			}
			persisted, err := journal.Get(ctx, op.ID)
			if err != nil || persisted.Status != "failed" || len(persisted.Steps) != 1 {
				t.Fatalf("restore validation after restart = %q (%v), want a failed validation", persisted.Status, err)
			}
			step := persisted.Steps[0]
			if step.Status != "failed" || step.Detail != "Validation was interrupted. Submit the archive and passphrase again." {
				t.Errorf("validation step after restart = %s/%q, want failure with retry instructions", step.Status, step.Detail)
			}
		})
	}
}

// TestRestartWorkspaceProvision loses the provider's creation reply, closes
// the database, and verifies that a new worker adopts the exact existing VM.
func TestRestartWorkspaceProvision(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "provision.db")}
	open := func() *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, OwnershipMarker: "fixture-owner"}
	owner := uuid.NewString()
	seedRecoveryRestart(t, db, ref, owner, now)
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET provider_instance_id=NULL,desired_state='running',
		observed_state='provisioning' WHERE id=$1`, ref.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
		VALUES($1,2,1024,40,'reserved','reserved',$2)`, ref.WorkspaceID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	op, err := journal.Create(ctx, operation.Request{
		Actor: owner, Key: "workspace-provision-restart", Type: "provision", OriginatingAction: "workspace.launch",
		WorkspaceID: &ref.WorkspaceID, Generation: ref.Generation, Steps: []operation.Step{{Key: "provision", Label: "Provision workspace"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	machines := &restartProvisionProvider{Provider: fake.New()}
	newWorker := func(db *sql.DB, journal *operation.Journal) *operation.Worker {
		launch := workspace.NewLaunchExecutor(workspace.NewStore(db), machines, nil, "", "", "", time.Now, nil)
		return workspace.NewWorker(journal, launch, nil)
	}
	machines.LoseReply("create")
	if err := newWorker(db, journal).RunOnce(ctx, op.ID); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("provision without reply returned %v, want uncertain outcome", err)
	}
	existing, err := machines.Inspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	journal = operation.NewJournal(db, time.Now, uuid.NewString)
	if err := newWorker(db, journal).RunOnce(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	done, err := journal.Get(ctx, op.ID)
	if err != nil || done.Status != "succeeded" || machines.creates != 1 {
		t.Errorf("provision after restart = %s, %d create requests (%v), want succeeded and one create", done.Status, machines.creates, err)
	}
	var instance, registration string
	if err := db.QueryRowContext(ctx, `SELECT w.provider_instance_id,g.state FROM workspaces w JOIN guest_registrations g
		ON w.id=g.workspace_id AND w.generation=g.generation WHERE w.id=$1`, ref.WorkspaceID).Scan(&instance, &registration); err != nil ||
		instance != existing.InstanceID || registration != "eligible" {
		t.Errorf("provisioned instance = %q/%q (%v), want existing VM and eligible guest", instance, registration, err)
	}
}

type restartProvisionProvider struct {
	*fake.Provider
	creates int
}

func (p *restartProvisionProvider) Create(ctx context.Context, request provider.CreateRequest) (provider.Inventory, error) {
	p.creates++
	return p.Provider.Create(ctx, request)
}

// TestRestartRecoveryReview reopens the controller database after each review
// decision, including a provider conflict, then checks the activation fence.
func TestRestartRecoveryReview(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "recover.db")}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, InstanceID: "retained-instance", OwnershipMarker: "fixture-owner"}
	actor := uuid.NewString()
	seedRecoveryRestart(t, db, ref, actor, now)
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	reviewID, err := backup.EnterRecovery(ctx, db, journal, ref.InstallationID, uuid.NewString(), actor, now)
	if err != nil {
		t.Fatal(err)
	}
	restart := func(wantReview, wantInstallation string, wantVersion int64) {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = database.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		journal = operation.NewJournal(db, time.Now, uuid.NewString)
		review, err := backup.ReadRecoveryReview(ctx, db, reviewID)
		if err != nil || review.State != wantReview || review.Version != wantVersion {
			t.Fatalf("review after restart = %s/%d (%v), want %s/%d", review.State, review.Version, err, wantReview, wantVersion)
		}
		var installed string
		if err := db.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&installed); err != nil || installed != wantInstallation {
			t.Fatalf("installation after restart = %q (%v), want %q", installed, err, wantInstallation)
		}
		var revoked bool
		if err := db.QueryRowContext(ctx, "SELECT revoked_at IS NOT NULL FROM sessions WHERE id='old-session'").Scan(&revoked); err != nil || !revoked {
			t.Fatalf("copied session revoked after restart = %t (%v), want true", revoked, err)
		}
	}
	update := func(decision backup.RecoveryDecisions) {
		t.Helper()
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := backup.UpdateRecoveryReview(ctx, tx, reviewID, actor, decision, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	observe := func(matches bool) []backup.RecoveryWorkspace {
		t.Helper()
		got, err := backup.ObserveRecoveryWorkspaces(ctx, db, ref.InstallationID, func(_ context.Context, observed provider.WorkspaceRef) (provider.Inventory, error) {
			if !matches || observed != ref {
				return provider.Inventory{}, provider.ErrOwnershipMismatch
			}
			return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Stopped,
				ArtifactID: "fixture-image", Release: "fixture", ObservedAt: now}, nil
		})
		if err != nil || len(got) != 1 {
			t.Fatalf("provider inventory after restart has %d entries (%v), want one", len(got), err)
		}
		return got
	}
	activate := func(fresh []backup.RecoveryWorkspace) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := backup.ActivateRecoveryInTx(ctx, tx, journal, reviewID, actor, "reviewed-restart-activation", fresh, now)
			return err
		})
	}

	restart("identity_review", "recovery", 1)
	update(backup.RecoveryDecisions{Section: "identities", Version: 1, UserIDs: []string{actor}})
	restart("provider_reconcile", "recovery", 2)
	update(backup.RecoveryDecisions{Section: "provider", Version: 2, Workspaces: observe(false)})
	restart("blocked", "recovery", 3)
	if err := activate(observe(false)); !errors.Is(err, backup.ErrRecoveryReview) {
		t.Fatalf("activate after unresolved provider restart = %v, want review rejection", err)
	}
	update(backup.RecoveryDecisions{Section: "provider", Version: 3, Workspaces: observe(true)})
	restart("authorization_review", "recovery", 4)
	update(backup.RecoveryDecisions{Section: "authorization_retention", Version: 4, AcceptRetention: true})
	restart("ready", "recovery", 5)
	if err := activate(observe(false)); !errors.Is(err, backup.ErrRecoveryReview) {
		t.Fatalf("activate with newly conflicting inventory = %v, want review rejection", err)
	}
	if err := activate(observe(true)); err != nil {
		t.Fatal(err)
	}
	restart("activated", "active", 6)
	var power string
	if err := db.QueryRowContext(ctx, "SELECT observed_state FROM workspaces WHERE id=$1", ref.WorkspaceID).Scan(&power); err != nil || power != "stopped" {
		t.Fatalf("restored workspace state = %q (%v), want verified stopped state", power, err)
	}
}

func seedRecoveryRestart(t *testing.T, db *sql.DB, ref provider.WorkspaceRef, actor string, now time.Time) {
	t.Helper()
	ctx, stamp := t.Context(), now.Format(time.RFC3339Nano)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Restart fixture','sqlite','https://management.test','{workspace}.ide.test',
		'{service}--{workspace}.preview.test','fixture','fixture',$2,$2,1)`, ref.InstallationID, stamp)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'owner@example.test','administrator','active','local','setup',$2,$2,1)`, actor, stamp)
	exec(`INSERT INTO sessions(id,token_digest,user_id,mode,csrf_digest,authenticated_at,expires_at)
		VALUES('old-session',$1,$2,'normal',$3,$4,$5)`, auth.TokenDigest("fixture login"), actor,
		auth.TokenDigest("fixture csrf"), stamp, now.Add(time.Hour).Format(time.RFC3339Nano))
	exec(`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('fixture-image','fixture','amd64','fixture','fixture','1','[]','{}','{}','{}',
		'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm',$1,$2,'available')`, actor, stamp)
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
		generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES($1,$2,$3,'fixture-template',1,'fixture-image','https://github.com/example/fixture','devcontainer.json',2,1024,40,
		'{"mode":"nat"}','stopped','running',1,$4,$5,$6,$6,1)`,
		ref.WorkspaceID, ref.InstallationID, actor, ref.OwnershipMarker, ref.InstanceID, stamp)
}

// TestRestartInitialAdministrator resumes both persisted setup steps after
// losing a controller before and after the corresponding executor effect.
func TestRestartInitialAdministrator(t *testing.T) {
	for _, tc := range []struct {
		name  string
		step  int
		apply bool
	}{
		{name: "administrator before observation", step: 0},
		{name: "administrator after observation", step: 0, apply: true},
		{name: "credentials before Dex write", step: 1},
		{name: "credentials after Dex write", step: 1, apply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkRestartInitialAdministrator(t, tc.step, tc.apply)
		})
	}
}

func checkRestartInitialAdministrator(t *testing.T, step int, apply bool) {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "setup.db")
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	seed := database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "Restart fixture", Origin: "https://127.0.0.1:8443",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "fixture", MinGuestRelease: "fixture", Now: now,
	}
	open := func() *sql.DB {
		t.Helper()
		db, _, err := database.OpenBootstrap(ctx, path, seed)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	journal := operation.NewJournal(db, clock, uuid.NewString)
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	intent := app.AdministratorIntent{InstallationID: seed.ID, UserID: uuid.NewString(),
		Email: "setup-restart@example.test", CreatedAt: now}
	admission, err := app.BeginAdministrator(ctx, db, journal, intent, cipher, "initial-owner-restart")
	if err != nil {
		t.Fatal(err)
	}
	dex := &restartPasswordStore{}
	executor := &app.AdministratorExecutor{DB: db, InstallationID: seed.ID, Admin: dex}
	worker := operation.Worker{Journal: journal, Name: "first-controller", LeaseDuration: time.Minute, Executor: executor}
	if step == 1 {
		if err := worker.RunOnce(ctx, admission.Operation.ID); err != nil {
			t.Fatal(err)
		}
	}
	claim, err := journal.Claim(ctx, admission.Operation.ID, worker.Name, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, step); err != nil {
		t.Fatal(err)
	}
	before, err := journal.Get(ctx, admission.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if apply {
		if _, err := executor.Apply(ctx, before, before.Steps[step]); err != nil {
			t.Fatal(err)
		}
	}
	var oldHash []byte
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM bootstrap_administrator").Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	reopened := open()
	restartedJournal := operation.NewJournal(reopened, clock, uuid.NewString)
	intent.UserID = uuid.NewString()
	retry, err := app.BeginAdministrator(ctx, reopened, restartedJournal, intent, cipher, "retry-owner-after-restart")
	if err != nil || retry.AdministratorID != admission.AdministratorID || retry.Operation.ID != admission.Operation.ID {
		t.Fatalf("restarted setup changed durable administrator or operation identity: %v", err)
	}
	finished, err := app.FinishAdministrator(ctx, reopened, restartedJournal, seed.ID, dex, retry.Operation)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("setup after restart = %q (%v), want succeeded", finished.Status, err)
	}
	for _, step := range finished.Steps {
		if step.Status != "succeeded" {
			t.Errorf("setup step %s = %s, want succeeded", step.Key, step.Status)
		}
	}
	if dex.writes != 1 || dex.userID != admission.AdministratorID || !bytes.Equal(oldHash, dex.hash) {
		t.Error("resuming setup changed the original credential or repeated the Dex write")
	}
}

type restartPasswordStore struct {
	writes int
	userID string
	hash   []byte
}

// TestRestartInterruptsIncompleteBackup starts from each durable backup state
// and checks that a new controller can admit a fresh backup without treating
// an interrupted checkpoint or encryption step as verified evidence.
func TestRestartInterruptsIncompleteBackup(t *testing.T) {
	for _, state := range []string{"preparing", "checkpointing", "encrypting"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "backup.db")}
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
				t.Fatal(err)
			}
			schema, err := database.CurrentSchemaVersion(cfg.Profile)
			if err != nil {
				t.Fatal(err)
			}
			metadata := backup.ArchiveMetadata{InstallationID: uuid.NewString(), Release: "fixture", Profile: cfg.Profile, Schema: schema}
			if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
				ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
				VALUES($1,'active','Restart fixture','sqlite','https://management.test','{workspace}.ide.test',
				'{service}--{workspace}.preview.test','fixture','fixture',$2,$2,1)`,
				metadata.InstallationID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			journal := operation.NewJournal(db, time.Now, uuid.NewString)
			service := backup.NewService(db, journal, metadata)
			op, err := service.QueueLocal(ctx, "first-backup-after-admission")
			if err != nil {
				t.Fatal(err)
			}
			if state != "preparing" {
				claim, err := journal.Claim(ctx, op.ID, "management-backup", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
					if err := journal.StartStepInTx(ctx, tx, claim, 0); err != nil {
						return err
					}
					if state == "encrypting" {
						if err := journal.CompleteStepInTx(ctx, tx, claim, 0, "Checkpoint captured."); err != nil {
							return err
						}
						if err := journal.StartStepInTx(ctx, tx, claim, 1); err != nil {
							return err
						}
					}
					_, err := tx.ExecContext(ctx, "UPDATE backup_records SET state=$1 WHERE operation_id=$2", state, op.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restartedJournal := operation.NewJournal(reopened, time.Now, uuid.NewString)
			restarted := backup.NewService(reopened, restartedJournal, metadata)
			if err := restarted.InterruptIncomplete(ctx); err != nil {
				t.Fatal(err)
			}
			var backupState, code string
			if err := reopened.QueryRowContext(ctx, "SELECT state,error_code FROM backup_records WHERE operation_id=$1",
				op.ID).Scan(&backupState, &code); err != nil || backupState != "failed" || code != "backup.interrupted" {
				t.Errorf("backup after restart = %q/%q (%v), want failed/backup.interrupted", backupState, code, err)
			}
			if got, err := restartedJournal.Get(ctx, op.ID); err != nil || got.Status != "failed" {
				t.Errorf("backup operation after restart = %q (%v), want failed", got.Status, err)
			}
			if ok, err := backup.HasCompletedBackup(ctx, reopened, metadata); err != nil || ok {
				t.Errorf("completed backup after interrupted %s = %t (%v), want false", state, ok, err)
			}
			fresh, err := restarted.QueueLocal(ctx, "new-backup-after-restart")
			if err != nil || fresh.ID == op.ID || fresh.Status != "pending" {
				t.Errorf("new backup after restart = %q (%v), want another pending operation", fresh.Status, err)
			}
		})
	}
}

func (s *restartPasswordStore) Ensure(_ context.Context, _ string, userID string, hash []byte) error {
	s.writes++
	s.userID = userID
	s.hash = bytes.Clone(hash)
	return nil
}
