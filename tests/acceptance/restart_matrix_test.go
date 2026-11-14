package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
// changes power but before the controller can record the result, for both
// successful guest updates and failed staging.
func TestRestartResumesMaintenancePower(t *testing.T) {
	t.Run("verified update", func(t *testing.T) { checkRestartMaintenancePower(t, true) })
	t.Run("failed staging", func(t *testing.T) { checkRestartMaintenancePower(t, false) })
}

func checkRestartMaintenancePower(t *testing.T, staged bool) {
	t.Helper()
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
	metadata := release.GuestUpdateMetadata{Release: "fixture", Protocol: "1", Digest: sha256.Sum256([]byte("pinned restart fixture"))}
	load := func(context.Context, string) (release.GuestUpdate, error) {
		return release.GuestUpdate{Metadata: metadata}, nil
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
	wantStatus, wantCampaign := "failed", "blocked"
	wantSteps := []string{"succeeded", "failed", "skipped", "skipped", "succeeded"}
	run := func(step string) {
		t.Helper()
		if err := worker.RunOnce(ctx, target); err != nil {
			t.Fatalf("RunOnce(%s) = %v, want success", step, err)
		}
	}
	if staged {
		wantStatus, wantCampaign = "succeeded", "completed"
		wantSteps = []string{"succeeded", "succeeded", "succeeded", "succeeded", "succeeded"}
		exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,update_digest)
			VALUES($1,1,'succeeded',$2)`, target, hex.EncodeToString(metadata.Digest[:]))
		run("stage update")
		exec("UPDATE guest_dispatches SET activation_status='succeeded' WHERE operation_id=$1", target)
		run("activate update")
		exec("UPDATE workspaces SET guest_release='fixture',protocol_version='1' WHERE id=$1", ref.WorkspaceID)
		run("verify update")
	} else {
		exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,problem)
			VALUES($1,1,'failed','guest.update_stage_failed')`, target)
		run("record staging failure")
	}
	interrupt("maintenance_restore", provider.Stopped)
	assertStates("running", "reserved")
	campaignWorker := store.MaintenanceCampaignWorker(journal, ref.InstallationID, clock)
	err = campaignWorker.RunOnce(ctx, campaign.ID)
	if _, ok := errors.AsType[*operation.StepWaiting](err); !ok {
		t.Fatalf("campaign before restoration is recorded = %v, want waiting", err)
	}
	run("verify restored power")
	assertStates("stopped", "released")
	op, err := journal.Get(ctx, target)
	if err != nil || op.Status != wantStatus || len(op.Steps) != len(wantSteps) {
		t.Fatalf("target after restart = %+v (%v), want %s with five steps", op, err, wantStatus)
	}
	for i, step := range op.Steps {
		if step.Status != wantSteps[i] {
			t.Errorf("restart step %s = %s, want %s", step.Key, step.Status, wantSteps[i])
		}
	}
	if err := campaignWorker.RunOnce(ctx, campaign.ID); err != nil {
		t.Fatal(err)
	}
	var campaignState string
	if err := db.QueryRowContext(ctx, "SELECT state FROM maintenance_campaigns WHERE operation_id=$1", campaign.ID).Scan(&campaignState); err != nil || campaignState != wantCampaign {
		t.Errorf("campaign after restored power = %s (%v), want %s", campaignState, err, wantCampaign)
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

// TestRestartResumesMaintenanceReceipts interrupts the controller after fixture
// receipts and after the campaign settles, before recording step completion.
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
			restart := func() {
				t.Helper()
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db = open()
				journal = operation.NewJournal(db, clock, uuid.NewString)
				store = workspace.NewStore(db)
			}
			const lease = time.Minute
			startStep := func(id string, index int) operation.Operation {
				t.Helper()
				claim, err := journal.Claim(ctx, id, "interrupted-controller", lease)
				if err != nil {
					t.Fatal(err)
				}
				if err := journal.StartStep(ctx, claim, index); err != nil {
					t.Fatal(err)
				}
				op, err := journal.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				return op
			}
			update := func() error {
				return store.MaintenanceUpdateWorker(journal, ref.InstallationID, "1", load).RunOnce(ctx, target)
			}
			interrupt := func(index int, persistReceipt func()) {
				t.Helper()
				startStep(target, index)
				persistReceipt()
				restart()
				now = now.Add(lease + time.Second)
				if err := update(); err != nil {
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
			})
			if activation == "succeeded" {
				restart()
				err := update()
				if _, ok := errors.AsType[*operation.StepWaiting](err); !ok {
					t.Fatalf("verification without the assigned guest release = %v, want waiting", err)
				}
				err = store.MaintenanceCampaignWorker(journal, ref.InstallationID, clock).RunOnce(ctx, campaign.ID)
				if _, ok := errors.AsType[*operation.StepWaiting](err); !ok {
					t.Fatalf("campaign with unverified target = %v, want waiting", err)
				}
				interrupt(2, func() {
					exec("UPDATE workspaces SET guest_release='fixture',protocol_version='1' WHERE id=$1", ref.WorkspaceID)
				})
			}
			wantCampaign := "blocked"
			if activation == "succeeded" {
				wantCampaign = "completed"
			}
			pending := startStep(campaign.ID, 0)
			campaignWorker := store.MaintenanceCampaignWorker(journal, ref.InstallationID, clock)
			// The executor commits the campaign state separately from the
			// worker's journal completion. Simulate losing the controller here.
			evidence, err := campaignWorker.Executor.Apply(ctx, pending, pending.Steps[0])
			if _, failed := errors.AsType[*operation.StepFailure](err); activation == "failed" && !failed ||
				activation == "succeeded" && (err != nil || evidence == "") {
				t.Fatalf("campaign settlement = %q (%v), want %s", evidence, err, wantCampaign)
			}
			restart()
			var state string
			if err := db.QueryRowContext(ctx, "SELECT state FROM maintenance_campaigns WHERE operation_id=$1", campaign.ID).Scan(&state); err != nil || state != wantCampaign {
				t.Fatalf("campaign state after restart = %s (%v), want %s", state, err, wantCampaign)
			}
			stillPending, err := journal.Get(ctx, campaign.ID)
			if err != nil || stillPending.Status != "running" || stillPending.Steps[0].Status != "active" {
				t.Fatalf("campaign journal after settlement = %+v (%v), want unfinished step", stillPending, err)
			}
			now = now.Add(lease + time.Second)
			if err := store.MaintenanceCampaignWorker(journal, ref.InstallationID, clock).RunOnce(ctx, campaign.ID); err != nil {
				t.Fatal(err)
			}
			restart()
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

// TestRestartWorkspaceLaunch loses provider replies and reopens the controller
// database across all launch steps, including while later hooks are pending.
func TestRestartWorkspaceLaunch(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, "DELETE FROM workspaces WHERE id=$1", ref.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	store := workspace.NewStore(db)
	allocation := workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
	if err := store.SetCapacityBudget(ctx, ref.InstallationID, allocation, 0, owner, now); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	var op operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		op, err = workspace.AdmitLaunch(ctx, tx, journal, workspace.LaunchIntent{
			InstallationID: ref.InstallationID, WorkspaceID: ref.WorkspaceID, OwnerID: owner, Name: "Restart fixture",
			TemplateID: "fixture-template", TemplateRevision: 1, ArtifactID: "fixture-image", RequestKey: "launch-restart-fixture",
			RepositoryURL: "https://github.com/example/fixture", DevcontainerPath: "devcontainer.json",
			Allocation: allocation, NetworkPolicyJSON: `{"mode":"nat"}`, OwnershipMarker: ref.OwnershipMarker,
		}, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	machines := &restartLaunchProvider{Provider: fake.New()}
	enrollment := &restartLaunchEnrollment{}
	ideReady := false
	ideUnavailable := errors.New("fixture IDE unavailable")
	probe := func(_ context.Context, id string, generation int64) error {
		if id != ref.WorkspaceID || generation != ref.Generation {
			t.Fatalf("IDE probe scope = %s/%d, want %s/%d", id, generation, ref.WorkspaceID, ref.Generation)
		}
		if !ideReady {
			return ideUnavailable
		}
		return nil
	}
	restart := func() *operation.Worker {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		journal = operation.NewJournal(db, time.Now, uuid.NewString)
		launch := workspace.NewLaunchExecutor(workspace.NewStore(db), machines, enrollment, "", "", "", time.Now, probe)
		return workspace.NewWorker(journal, launch, nil)
	}
	run := func(step string, want error) {
		t.Helper()
		worker := restart()
		if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, want) {
			t.Fatalf("RunOnce(%s) = %v, want %v", step, err, want)
		}
	}
	machines.LoseReply("create")
	run("provision without reply", provider.ErrOutcomeUnknown)
	existing, err := machines.Inspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	run("adopt provisioned VM", nil)
	checkStep := func(index int, step, status, stepStatus string) {
		t.Helper()
		current, err := journal.Get(ctx, op.ID)
		if err != nil || current.Status != status || len(current.Steps) <= index ||
			current.Steps[index].Key != step || current.Steps[index].Status != stepStatus {
			t.Fatalf("launch after %s = %s, steps %+v (%v), want %s with step %s", step, current.Status, current.Steps, err, status, stepStatus)
		}
	}
	checkStep(0, "provision", "waiting", "succeeded")
	var instance, registration string
	if err := db.QueryRowContext(ctx, `SELECT w.provider_instance_id,g.state FROM workspaces w JOIN guest_registrations g
		ON w.id=g.workspace_id AND w.generation=g.generation WHERE w.id=$1`, ref.WorkspaceID).Scan(&instance, &registration); err != nil ||
		instance != existing.InstanceID || registration != "eligible" {
		t.Errorf("provisioned instance = %q/%q (%v), want existing VM and eligible guest", instance, registration, err)
	}
	machines.LoseReply("start")
	run("guest boot without reply", provider.ErrOutcomeUnknown)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen without an authenticated poll and verify the worker stays in
	// enrollment. The fixture then supplies the receipt normally written by
	// the enrollment and poll endpoints.
	if err := restart().RunOnce(ctx, op.ID); err == nil {
		t.Fatal("enrollment succeeded before authenticated guest contact")
	}
	checkStep(1, "enroll", "waiting", "active")
	expiry := now.Add(time.Hour).Format(time.RFC3339Nano)
	exec(`UPDATE guest_registrations SET state='active',public_key_fingerprint='fixture-key',
		certificate_not_after=$1,last_authenticated_at=$2 WHERE workspace_id=$3`, expiry, now.Format(time.RFC3339Nano), ref.WorkspaceID)
	exec(`INSERT INTO enrollment_grants(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state,key_proof,certificate_der)
		VALUES($1,$2,$3,$4,$5,$6,'consumed','fixture-key','public')`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ref.WorkspaceID, ref.Generation, op.ID, existing.InstanceID, expiry)
	run("verify enrollment", nil)
	checkStep(1, "enroll", "waiting", "succeeded")
	if err := restart().RunOnce(ctx, op.ID); err == nil {
		t.Fatal("configuration succeeded before the connection-ready receipt")
	}
	checkStep(2, "configure", "waiting", "active")
	exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id)
		VALUES($1,1,'running','fixture-container')`, op.ID)
	run("consume connection-ready receipt", nil)
	checkStep(2, "configure", "waiting", "succeeded")
	run("probe unavailable IDE", ideUnavailable)
	checkStep(3, "verify_ide", "waiting", "active")
	ideReady = true
	run("verify IDE", nil)
	checkStep(3, "verify_ide", "waiting", "succeeded")
	checkStep(4, "finish_setup", "waiting", "pending")
	if err := restart().RunOnce(ctx, op.ID); err == nil {
		t.Fatal("setup finished before the final guest receipt")
	}
	checkStep(4, "finish_setup", "waiting", "active")
	exec("UPDATE guest_dispatches SET status='succeeded' WHERE operation_id=$1", op.ID)
	run("finish setup hooks", nil)
	run("completed launch replay", operation.ErrConflict)
	checkStep(4, "finish_setup", "succeeded", "succeeded")
	if machines.creates != 1 || machines.starts != 1 || enrollment.deliveries != 1 || enrollment.cleanups != 1 {
		t.Errorf("launch effects = creates %d, starts %d, deliveries %d, cleanups %d, want one of each",
			machines.creates, machines.starts, enrollment.deliveries, enrollment.cleanups)
	}
}

// TestRestartWorkspaceRepairAndRetry interrupts a failed rebuild and its
// repair workflow at each active step before consuming the guest evidence.
func TestRestartWorkspaceRepairAndRetry(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "repair.db")}
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
	machines := fake.New()
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, OwnershipMarker: "fixture-owner"}
	allocation := workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40}
	vm, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "repair-fixture-create",
		ArtifactID: "fixture-image", Release: "fixture", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := machines.Start(ctx, provider.StartRequest{Ref: vm.Ref, OperationID: "repair-fixture-start"})
	if err != nil {
		t.Fatal(err)
	}
	ref = before.Ref
	owner := uuid.NewString()
	seedRecoveryRestart(t, db, ref, owner, now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE workspaces SET desired_state='running' WHERE id=$1", ref.WorkspaceID)
	exec(`INSERT INTO guest_registrations(workspace_id,generation,state,certificate_not_after,last_authenticated_at)
		VALUES($1,1,'active',$2,$3)`, ref.WorkspaceID, now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err := workspace.NewStore(db).SetCapacityBudget(ctx, ref.InstallationID, allocation, 0, owner, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return workspace.ReserveStart(ctx, tx, ref.InstallationID, ref.WorkspaceID, ref.Generation, now)
	}); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	admit := func(action string) operation.Operation {
		t.Helper()
		// Separate lifecycle timestamps so repair checks can identify the latest operation.
		now = now.Add(time.Second)
		var op operation.Operation
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var version int64
			if err := tx.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", ref.WorkspaceID).Scan(&version); err != nil {
				return err
			}
			var err error
			op, err = workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{WorkspaceID: ref.WorkspaceID,
				ActorID: owner, RequestKey: uuid.NewString(), Action: action, Version: version}, now)
			return err
		})
		if err != nil {
			t.Fatalf("AdmitPower(%s): %v", action, err)
		}
		return op
	}
	step := func(op operation.Operation, index int, evidence func()) {
		t.Helper()
		key := op.Steps[index].Key
		claim, err := journal.Claim(ctx, op.ID, "interrupted-controller", time.Second)
		if err != nil {
			t.Fatalf("claim %s/%s before restart: %v", op.Type, key, err)
		}
		if err := journal.StartStep(ctx, claim, index); err != nil {
			t.Fatalf("start %s/%s before restart: %v", op.Type, key, err)
		}
		if evidence != nil {
			evidence()
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		journal = operation.NewJournal(db, clock, uuid.NewString)
		probe := func(_ context.Context, id string, generation int64) error {
			if id != ref.WorkspaceID || generation != ref.Generation {
				return errors.New("IDE probe reached the wrong workspace generation")
			}
			return nil
		}
		power := workspace.NewPowerExecutor(workspace.NewStore(db), machines, clock, probe)
		power.SetRepairProbe(probe)
		worker := workspace.NewWorker(journal, nil, power)
		if err := worker.RunOnce(ctx, op.ID); !errors.Is(err, operation.ErrConflict) {
			t.Fatalf("resume %s/%s with outstanding lease = %v, want conflict", op.Type, key, err)
		}
		now = now.Add(2 * time.Second)
		if err := worker.RunOnce(ctx, op.ID); err != nil {
			t.Fatalf("resume %s/%s after lease expiry: %v", op.Type, key, err)
		}
	}
	check := func(op operation.Operation, want string) {
		t.Helper()
		got, err := journal.Get(ctx, op.ID)
		if err != nil || got.Status != want {
			t.Fatalf("%s after restart = %s (%v), want %s", op.Type, got.Status, err, want)
		}
	}
	rebuild := admit("rebuild")
	step(rebuild, 0, func() {
		exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,problem)
			VALUES($1,1,'failed','guest.required_setup_failed')`, rebuild.ID)
	})
	check(rebuild, "failed")
	repair := admit("repair")
	step(repair, 0, func() {
		exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id)
			VALUES($1,1,'succeeded','repair-container')`, repair.ID)
	})
	step(repair, 1, nil)
	check(repair, "succeeded")
	retry := admit("retry")
	step(retry, 0, func() {
		exec(`INSERT INTO guest_dispatches(operation_id,dispatch_version,status,container_id)
			VALUES($1,1,'running','development-container')`, retry.ID)
	})
	step(retry, 1, nil)
	step(retry, 2, func() {
		exec("UPDATE guest_dispatches SET status='succeeded' WHERE operation_id=$1", retry.ID)
	})
	check(retry, "succeeded")
	after, err := machines.Inspect(ctx, ref)
	if err != nil || after.Ref != before.Ref || after.Power != provider.Running || !slices.Equal(after.Resources, before.Resources) {
		t.Errorf("retained VM after repair and retry = %+v (%v), want original running VM and resources", after, err)
	}
}

type restartLaunchProvider struct {
	*fake.Provider
	creates, starts int
}

func (p *restartLaunchProvider) Create(ctx context.Context, request provider.CreateRequest) (provider.Inventory, error) {
	p.creates++
	request.EnrollmentMedia = "/empty-fixture.iso"
	return p.Provider.Create(ctx, request)
}

func (p *restartLaunchProvider) Start(ctx context.Context, request provider.StartRequest) (provider.Inventory, error) {
	p.starts++
	return p.Provider.Start(ctx, request)
}

type restartLaunchEnrollment struct {
	deliveries, cleanups int
}

func (e *restartLaunchEnrollment) Deliver(ctx context.Context, op string, vm provider.Inventory, _ time.Time, _, _, _ string,
	attach func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error)) (string, error) {
	e.deliveries++
	const media = "/fixture-enrollment.iso"
	_, err := attach(ctx, provider.EnrollmentRequest{Ref: vm.Ref, OperationID: op, MediaPath: media, Eligible: true})
	return media, err
}

func (e *restartLaunchEnrollment) CleanupDelivery(context.Context, string, provider.WorkspaceRef, string) error {
	e.cleanups++
	return nil
}

// TestRestartWorkspaceDeletion reopens the controller during graceful
// shutdown, uncertain stop, and partial and uncertain removal. Only
// retention authorizes a forced stop, and only after its grace window.
func TestRestartWorkspaceDeletion(t *testing.T) {
	t.Run("explicit", func(t *testing.T) { checkRestartWorkspaceDeletion(t, false) })
	t.Run("retention", func(t *testing.T) { checkRestartWorkspaceDeletion(t, true) })
}

func checkRestartWorkspaceDeletion(t *testing.T, retention bool) {
	t.Helper()
	ctx, now := t.Context(), time.Now().UTC()
	clock := func() time.Time { return now }
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "deletion.db")}
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
	machines := &restartDeletionProvider{Provider: fake.New(), now: clock}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, OwnershipMarker: "fixture-owner"}
	created, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create",
		ArtifactID: "fixture-image", Release: "fixture", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	ref = created.Ref
	if _, err := machines.Start(ctx, provider.StartRequest{Ref: ref, OperationID: "fixture-start"}); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	seedRecoveryRestart(t, db, ref, owner, now.Add(-time.Hour))
	exec("UPDATE workspaces SET desired_state='running' WHERE id=$1", ref.WorkspaceID)
	exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at)
		VALUES($1,2,1024,40,'reserved','reserved',$2)`, ref.WorkspaceID, now.Format(time.RFC3339Nano))
	exec("INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,$2,'active')", ref.WorkspaceID, ref.Generation)
	journal := operation.NewJournal(db, clock, uuid.NewString)
	restart := func() {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		journal = operation.NewJournal(db, clock, uuid.NewString)
	}
	var deletion operation.Operation
	if retention {
		exec("UPDATE workspaces SET retention_seconds=600 WHERE id=$1", ref.WorkspaceID)
		maximum := int64(3600)
		policy := workspace.RetentionEligibility{MaximumSeconds: &maximum, ReconciledAt: now, OutageGrace: time.Minute}
		restart()
		list, err := workspace.NewStore(db).RetentionCandidates(ctx, ref.InstallationID, now, policy, machines.Inspect)
		if err != nil || len(list) != 0 {
			t.Fatalf("retention during outage grace = %d candidates (%v), want zero", len(list), err)
		}
		now = now.Add(policy.OutageGrace)
		store := workspace.NewStore(db)
		list, err = store.RetentionCandidates(ctx, ref.InstallationID, now, policy, machines.Inspect)
		if err != nil || len(list) != 1 || list[0].Ref != ref {
			t.Fatalf("retention after outage grace = %+v (%v), want the original workspace", list, err)
		}
		deletion, err = store.CommitRetentionDeletion(ctx, journal, list[0], now, policy)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			deletion, err = workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{
				WorkspaceID: ref.WorkspaceID, ActorID: owner, Action: "delete", RequestKey: "delete-with-restart", Version: 1,
			}, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	const lease = 30 * time.Second
	claim, err := journal.Claim(ctx, deletion.ID, "interrupted-controller", lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	if !retention {
		exec("INSERT INTO guest_dispatches(operation_id,dispatch_version,status) VALUES($1,1,'succeeded')", deletion.ID)
	}
	runOnce := func() error {
		power := workspace.NewPowerExecutor(workspace.NewStore(db), machines, clock, nil)
		return workspace.NewWorker(journal, nil, power).RunOnce(ctx, deletion.ID)
	}
	run := func(step string, want error) {
		t.Helper()
		if err := runOnce(); !errors.Is(err, want) {
			t.Fatalf("RunOnce(%s) = %v, want %v", step, err, want)
		}
	}
	assertState := func(wantObserved, wantCPU, wantDisk, wantGuest string) {
		t.Helper()
		var desired, observed, cpu, disk, guestState string
		err := db.QueryRowContext(ctx, `SELECT w.desired_state,w.observed_state,r.cpu_memory_state,r.disk_state,g.state
			FROM workspaces w JOIN reservations r ON r.workspace_id=w.id
			JOIN guest_registrations g ON g.workspace_id=w.id AND g.generation=w.generation WHERE w.id=$1`,
			ref.WorkspaceID).Scan(&desired, &observed, &cpu, &disk, &guestState)
		if err != nil || desired != "deleted" || observed != wantObserved || cpu != wantCPU || disk != wantDisk || guestState != wantGuest {
			t.Fatalf("deletion state = %s/%s %s/%s guest=%s (%v), want deleted/%s %s/%s guest=%s",
				desired, observed, cpu, disk, guestState, err, wantObserved, wantCPU, wantDisk, wantGuest)
		}
	}
	restart()
	run("shutdown before lease expiry", operation.ErrConflict)
	if retention {
		now = now.Add(59 * time.Second)
		err := runOnce()
		if _, ok := errors.AsType[*operation.StepWaiting](err); !ok || machines.stops != 0 {
			t.Fatalf("shutdown before grace expiry = %v after %d stops, want waiting and no stop", err, machines.stops)
		}
		assertState("running", "reserved", "reserved", "active")
		restart()
		now = now.Add(time.Second)
	} else {
		now = now.Add(lease + time.Second)
	}
	run("shutdown", nil)
	assertState("running", "reserved", "reserved", "active")
	machines.LoseReply("stop")
	run("stop without reply", provider.ErrOutcomeUnknown)
	restart()
	assertState("running", "reserved", "reserved", "active")
	run("verify stopped", nil)
	restart()
	assertState("stopped", "released", "reserved", "active")
	machines.FailDelete()
	run("partial deletion", provider.ErrPartial)
	restart()
	assertState("stopped", "released", "reserved", "active")
	partial, err := journal.Get(ctx, deletion.ID)
	if err != nil || partial.Status != "waiting" || len(partial.Steps) != 3 || partial.Steps[2].Status != "active" {
		t.Fatalf("partial deletion after restart = %s, steps %+v (%v), want waiting with active delete", partial.Status, partial.Steps, err)
	}
	machines.LoseReply("delete")
	run("deletion without reply", provider.ErrOutcomeUnknown)
	restart()
	assertState("stopped", "released", "reserved", "active")
	run("verify deletion", nil)
	restart()
	assertState("deleted", "released", "released", "deleted")
	finished, err := journal.Get(ctx, deletion.ID)
	if err != nil || finished.Status != "succeeded" || machines.stops != 1 || machines.deletes != 2 || machines.forcedStop != retention {
		t.Fatalf("deletion after restart = %s (%v), stops=%d deletes=%d force=%t, want success, one stop, two delete attempts, force=%t",
			finished.Status, err, machines.stops, machines.deletes, machines.forcedStop, retention)
	}
	for _, step := range finished.Steps {
		if step.Status != "succeeded" {
			t.Errorf("step %s = %s, want succeeded", step.Key, step.Status)
		}
	}
	events, err := journal.Events(ctx, deletion.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("Events(deletion) = %d (%v), want recorded workflow", len(events), err)
	}
	last := events[len(events)-1]
	if last.Operation.Status != "succeeded" || last.Evidence != "Provider verified that the workspace and its owned resources were removed." {
		t.Errorf("final deletion event = %s/%q, want success with verified removal evidence", last.Operation.Status, last.Evidence)
	}
}

type restartDeletionProvider struct {
	*fake.Provider
	now            func() time.Time
	stops, deletes int
	forcedStop     bool
}

func (p *restartDeletionProvider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	inventory, err := p.Provider.Inspect(ctx, ref)
	inventory.ObservedAt = p.now()
	return inventory, err
}

func (p *restartDeletionProvider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	p.stops++
	p.forcedStop = p.forcedStop || req.ForceRetentionDelete
	return p.Provider.Stop(ctx, req)
}

func (p *restartDeletionProvider) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	p.deletes++
	return p.Provider.Delete(ctx, req)
}

// TestRestartRestorePublication abandons a private replacement after the
// recovery transaction and retries from the durable validation record.
func TestRestartRestorePublication(t *testing.T) {
	ctx, now := t.Context(), time.Now().UTC()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	open := func(path string) *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: path})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	sourcePath := filepath.Join(dir, "source.db")
	source := open(sourcePath)
	if err := database.Migrate(ctx, source, "sqlite"); err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(),
		Generation: 1, InstanceID: "retained-instance", OwnershipMarker: "fixture-owner"}
	actor := uuid.NewString()
	seedRecoveryRestart(t, source, ref, actor, now)
	schema, err := database.CurrentSchemaVersion("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	metadata := backup.ArchiveMetadata{InstallationID: ref.InstallationID, Release: "fixture", Profile: "sqlite",
		Schema: schema, HostID: "restart-fixture-host"}
	const passphrase = "fixture restore passphrase"
	archivePath := filepath.Join(dir, "management.age")
	if _, err := backup.CaptureArchive(ctx, archivePath, passphrase, metadata, backupRestartCheckpoint(source)); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "replacement")
	target := sha256.Sum256([]byte(destination))
	targetDigest := hex.EncodeToString(target[:])
	expected := backup.RestoreRequirements{Metadata: metadata}
	checkTarget := func(context.Context) error {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return backup.ErrRestoreTarget
		}
		return nil
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := backup.CreateRestorePlan(ctx, dir, targetDigest, archive, passphrase, expected, checkTarget)
	if err := errors.Join(err, archive.Close()); err != nil {
		t.Fatal(err)
	}
	restage := func() string {
		t.Helper()
		loaded, err := backup.LoadRestorePlan(dir, plan.ID)
		if err != nil || loaded != plan {
			t.Fatalf("LoadRestorePlan after restart changed the validation record: %v", err)
		}
		archive, err := os.Open(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		stage, err := backup.StageRestore(ctx, dir, loaded, targetDigest, archive, passphrase, expected, checkTarget)
		if err := errors.Join(err, archive.Close()); err != nil {
			t.Fatal(err)
		}
		return stage
	}
	abandoned := restage()
	prepared := open(filepath.Join(abandoned, "application.db"))
	journal := operation.NewJournal(prepared, time.Now, uuid.NewString)
	abandonedReview, err := backup.EnterRecovery(ctx, prepared, journal, ref.InstallationID, plan.ID, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(prepared.Close(), source.Close()); err != nil {
		t.Fatal(err)
	}
	if err := checkTarget(ctx); err != nil {
		t.Fatal("the interrupted restore published its private replacement")
	}
	stage := restage()
	reviewID, err := backup.ActivateSQLiteRestore(ctx, stage, destination, ref.InstallationID, plan.ID, actor, now)
	if err != nil || reviewID == "" || reviewID == abandonedReview {
		t.Fatalf("activation after restart = %q (%v), want a newly reviewed replacement", reviewID, err)
	}
	if _, err := backup.ActivateSQLiteRestore(ctx, abandoned, destination, ref.InstallationID, plan.ID, actor, now); !errors.Is(err, backup.ErrRestoreTarget) {
		t.Fatalf("activation replay = %v, want occupied target rejection", err)
	}
	assertAuthority := func(db *sql.DB, wantState string, wantRevoked bool) {
		t.Helper()
		var state string
		var revoked bool
		err := db.QueryRowContext(ctx, `SELECT i.state,s.revoked_at IS NOT NULL FROM installations i CROSS JOIN sessions s
			WHERE i.id=$1 AND s.id='old-session'`, ref.InstallationID).Scan(&state, &revoked)
		if err != nil || state != wantState || revoked != wantRevoked {
			t.Errorf("installation authority = %s, revoked=%t (%v), want %s, revoked=%t", state, revoked, err, wantState, wantRevoked)
		}
	}
	assertAuthority(open(sourcePath), "active", false)
	replacement := open(filepath.Join(destination, "application.db"))
	assertAuthority(replacement, "recovery", true)
	review, err := backup.ReadRecoveryReview(ctx, replacement, reviewID)
	if err != nil || review.State != "identity_review" {
		t.Errorf("published recovery review after reopening = %q (%v), want identity_review", review.State, err)
	}
	var reviews, completed int
	err = replacement.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(CASE WHEN o.status='succeeded'
		AND o.originating_action='restore.activate' AND o.request_key=$1 THEN 1 END)
		FROM recovery_reviews r JOIN operations o ON o.id=r.restore_operation_id`, plan.ID).Scan(&reviews, &completed)
	if err != nil || reviews != 1 || completed != 1 {
		t.Errorf("published replacement has %d reviews and %d completed restore operations (%v), want one of each", reviews, completed, err)
	}
}

// TestRestartRecoveryReview reopens the controller database after each review
// decision, including a provider conflict and a rolled-back final approval.
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
	const approvalKey = "reviewed-restart-activation"
	activate := func(fresh []backup.RecoveryWorkspace) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := backup.ActivateRecoveryInTx(ctx, tx, journal, reviewID, actor, approvalKey, fresh, now)
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
	session, _, err := auth.NewSession("review-session", actor, "recovery_review", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	// The audit write is last. Interrupting it must roll back authority,
	// workspace observations, session retirement, and operation completion.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER interrupt_recovery_approval BEFORE INSERT ON audit_events
		WHEN NEW.action='recovery.activate' BEGIN SELECT RAISE(ABORT, 'fixture audit write interruption'); END`); err != nil {
		t.Fatal(err)
	}
	if err := activate(observe(true)); err == nil || errors.Is(err, backup.ErrRecoveryReview) {
		t.Fatalf("interrupted approval = %v, want database failure at the audit write", err)
	}
	restart("ready", "recovery", 5)
	var approvals int
	var power string
	var reviewRevoked bool
	err = db.QueryRowContext(ctx, `SELECT w.observed_state,s.revoked_at IS NOT NULL,
		(SELECT COUNT(*) FROM operations WHERE request_key=$1)
		FROM workspaces w CROSS JOIN sessions s WHERE w.id=$2 AND s.id=$3`,
		approvalKey, ref.WorkspaceID, session.ID).Scan(&power, &reviewRevoked, &approvals)
	if err != nil || power != "running" || reviewRevoked || approvals != 0 {
		t.Fatalf("interrupted approval left %s, revoked=%t, %d operations (%v), want running, open review session, and no approval",
			power, reviewRevoked, approvals, err)
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER interrupt_recovery_approval"); err != nil {
		t.Fatal(err)
	}
	if err := activate(observe(true)); err != nil {
		t.Fatal(err)
	}
	restart("activated", "active", 6)
	if err := db.QueryRowContext(ctx, "SELECT observed_state FROM workspaces WHERE id=$1", ref.WorkspaceID).Scan(&power); err != nil || power != "stopped" {
		t.Fatalf("restored workspace state = %q (%v), want verified stopped state", power, err)
	}
	var reason string
	err = db.QueryRowContext(ctx, `SELECT s.revocation_reason, (SELECT COUNT(*) FROM operations
		WHERE request_key=$1 AND status='succeeded' AND originating_action='recovery.activate')
		FROM sessions s WHERE s.id=$2 AND s.revoked_at IS NOT NULL`, approvalKey, session.ID).Scan(&reason, &approvals)
	if err != nil || reason != "recovery_activated" || approvals != 1 {
		t.Errorf("final approval = %d succeeded, review session reason %q (%v), want one approval and recovery_activated", approvals, reason, err)
	}
	var audits int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action='recovery.activate'
		AND resource_id=$1 AND actor_id=$2 AND outcome='activated'`, reviewID, actor).Scan(&audits)
	if err != nil || audits != 1 {
		t.Errorf("recovery approval audit after restart = %d (%v), want one", audits, err)
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

// TestRestartBackup checks startup at every durable backup state, including
// when an encrypted file was published but its journal transaction failed.
func TestRestartBackup(t *testing.T) {
	for _, state := range []string{"preparing", "checkpointing", "encrypting", "published", "completed"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "backup.db")}
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
			var verified backup.ArchiveEvidence
			const passphrase = "fixture backup passphrase"
			output := filepath.Join(dir, "management.age")
			switch state {
			case "published", "completed":
				if state == "published" {
					// The file is already durable when Run tries this update.
					if _, err := db.ExecContext(ctx, `CREATE TRIGGER interrupt_backup_completion BEFORE UPDATE OF state ON backup_records
						WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT, 'fixture journal failure'); END`); err != nil {
						t.Fatal(err)
					}
				}
				evidence, err := service.Run(ctx, op.ID, output, passphrase, backupRestartCheckpoint(db))
				if state == "completed" && err != nil {
					t.Fatal(err)
				}
				if state == "published" && (err == nil || err.Error() != "encrypted archive was published but its completion could not be recorded") {
					t.Fatalf("Run with interrupted completion = %v, want published archive with uncommitted completion", err)
				}
				file, err := os.Open(output)
				if err != nil {
					t.Fatal(err)
				}
				verified, err = backup.VerifyArchive(ctx, file, passphrase)
				if err := errors.Join(err, file.Close()); err != nil {
					t.Fatalf("VerifyArchive(%s) = %v, want a readable, encrypted archive", state, err)
				}
				if verified.Metadata != metadata || (state == "completed" && verified != evidence) {
					t.Fatal("published archive evidence does not match the backup attempt")
				}
			case "checkpointing", "encrypting":
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
			for range 2 {
				if err := restarted.InterruptIncomplete(ctx); err != nil {
					t.Fatal(err)
				}
			}
			completed := state == "completed"
			wantState, wantOperation, wantCode := "failed", "failed", "backup.interrupted"
			if completed {
				wantState, wantOperation, wantCode = "completed", "succeeded", ""
				var manifest, digest string
				var size int64
				if err := reopened.QueryRowContext(ctx, "SELECT manifest_digest,archive_digest,size_bytes FROM backup_records WHERE operation_id=$1",
					op.ID).Scan(&manifest, &digest, &size); err != nil || manifest != verified.ManifestDigest || digest != verified.ArchiveDigest || size != verified.Size {
					t.Errorf("completed archive evidence after restart = %q/%q/%d (%v), want %q/%q/%d",
						manifest, digest, size, err, verified.ManifestDigest, verified.ArchiveDigest, verified.Size)
				}
			}
			if state == "published" || completed {
				file, err := os.Stat(output)
				if err != nil || file.Size() != verified.Size {
					t.Errorf("published archive changed during startup recovery: %v", err)
				}
			}
			var backupState, code string
			if err := reopened.QueryRowContext(ctx, "SELECT state,COALESCE(error_code,'') FROM backup_records WHERE operation_id=$1",
				op.ID).Scan(&backupState, &code); err != nil || backupState != wantState || code != wantCode {
				t.Errorf("backup after restart = %q/%q (%v), want %q/%q", backupState, code, err, wantState, wantCode)
			}
			if got, err := restartedJournal.Get(ctx, op.ID); err != nil || got.Status != wantOperation {
				t.Errorf("backup operation after restart = %q (%v), want %q", got.Status, err, wantOperation)
			}
			if ok, err := backup.HasCompletedBackup(ctx, reopened, metadata); err != nil || ok != completed {
				t.Errorf("completed backup after restart from %s = %t (%v), want %t", state, ok, err, completed)
			}
			replay, err := restarted.QueueLocal(ctx, op.RequestKey)
			if err != nil || replay.ID != op.ID || replay.Status != wantOperation {
				t.Errorf("replay after restart = %s/%q (%v), want original backup in %q", replay.ID, replay.Status, err, wantOperation)
			}
			fresh, err := restarted.QueueLocal(ctx, "new-backup-after-restart")
			if err != nil || fresh.ID == op.ID || fresh.Status != "pending" {
				t.Errorf("new backup after restart = %q (%v), want another pending operation", fresh.Status, err)
			}
		})
	}
}

func backupRestartCheckpoint(db *sql.DB) func(context.Context, string) (map[string]string, error) {
	return func(ctx context.Context, stage string) (map[string]string, error) {
		application := filepath.Join(stage, "application.db")
		if err := backup.SQLiteCheckpoint(ctx, db, application); err != nil {
			return nil, err
		}
		// Restart tests open the application checkpoint. They don't start
		// Dex or use the protected fixture files to run a controller.
		sources := map[string]string{"application.db": application}
		for _, name := range []string{"configuration.json", "guest-ca.json", "installation.key", "dex.db"} {
			path := filepath.Join(stage, name)
			payload := []byte(name)
			if name == "installation.key" {
				payload = bytes.Repeat([]byte{1}, 32)
			}
			if err := os.WriteFile(path, payload, 0600); err != nil {
				return nil, err
			}
			sources[name] = path
		}
		return sources, nil
	}
}

func (s *restartPasswordStore) Ensure(_ context.Context, _ string, userID string, hash []byte) error {
	s.writes++
	s.userID = userID
	s.hash = bytes.Clone(hash)
	return nil
}
