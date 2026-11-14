package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

var errGuestShutdownPending = errors.New("waiting for guest workload shutdown")

// PowerExecutor reconciles start, graceful stop, and container rebuild on a VM.
type PowerExecutor struct {
	store               *Store
	provider            provider.Provider
	now                 func() time.Time
	probeIDE            IDEProbe
	probeRepair         IDEProbe
	recovery            *LaunchExecutor
	closeDeletionAccess func(string, bool) error
}

// NewPowerExecutor binds retained workspace power operations to the current provider.
func NewPowerExecutor(store *Store, machines provider.Provider, now func() time.Time, probe IDEProbe) *PowerExecutor {
	return &PowerExecutor{store: store, provider: machines, now: now, probeIDE: probe}
}

// SetRepairProbe binds the authenticated repair service readiness check.
func (e *PowerExecutor) SetRepairProbe(probe IDEProbe) { e.probeRepair = probe }

// SetDeletionAccessCloser binds the registry used during deletion reconciliation.
func (e *PowerExecutor) SetDeletionAccessCloser(closeAccess func(string, bool) error) {
	e.closeDeletionAccess = closeAccess
}

// Observe inspects the exact VM before any new provider action is attempted.
func (e *PowerExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	req, err := e.request(ctx, op, step)
	if err != nil {
		return "", false, err
	}
	got, err := e.provider.Inspect(ctx, req.Ref)
	if err != nil {
		return "", false, err
	}
	if req.Ref.InstanceID == "" && op.OriginatingAction == "user.disable" {
		req.Ref.InstanceID = got.InstanceID
	}
	if err := verifyWorkspaceVM(req, got); err != nil {
		return "", false, err
	}
	if got.Power != provider.Running && got.Power != provider.Stopped {
		return "", false, provider.ErrOutcomeUnknown
	}
	if step.Key == "shutdown" {
		if got.Power == provider.Stopped {
			return "Provider confirmed the VM was already stopped.", true, nil
		}
		evidence, err := e.shutdownEvidence(ctx, op)
		return evidence, err == nil, err
	}
	if step.Key == "configure" || step.Key == "finish_setup" {
		if got.Power != provider.Running {
			return "", false, provider.ErrOutcomeUnknown
		}
		evidence, err := configurationEvidence(ctx, e.store.db, op, step)
		return evidence, err == nil, err
	}
	if step.Key == "verify_ide" {
		if got.Power != provider.Running {
			return "", false, provider.ErrOutcomeUnknown
		}
		evidence, err := verifyIDE(ctx, e.store.db, op, e.probeIDE)
		return evidence, err == nil, err
	}
	if op.Type == "repair" {
		if got.Power != provider.Running {
			return "", false, provider.ErrOutcomeUnknown
		}
		evidence, err := e.repairEvidence(ctx, op, step)
		return evidence, err == nil, err
	}
	if (got.Power == provider.Running) != (op.Type == "start") {
		return "", false, nil
	}
	evidence, err := e.record(ctx, op, req, got)
	return evidence, err == nil, err
}

// Apply rechecks current workspace authority and requests the provider action.
func (e *PowerExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	req, err := e.request(ctx, op, step)
	if err != nil {
		return "", err
	}
	if step.Key == "shutdown" {
		return e.shutdownEvidence(ctx, op)
	}
	if step.Key == "configure" || step.Key == "finish_setup" {
		return configurationEvidence(ctx, e.store.db, op, step)
	}
	if step.Key == "verify_ide" {
		return verifyIDE(ctx, e.store.db, op, e.probeIDE)
	}
	if op.Type == "repair" {
		return e.repairEvidence(ctx, op, step)
	}
	var got provider.Inventory
	if op.Type == "start" {
		got, err = e.start(ctx, op, step, req)
	} else {
		if req.Ref.InstanceID == "" {
			// A launch may have imported the VM before recording its instance.
			// Bind the stop to the fully matched provider identity.
			inventory, err := e.provider.Inspect(ctx, req.Ref)
			if err != nil {
				return "", err
			}
			req.Ref.InstanceID = inventory.InstanceID
			if err := verifyWorkspaceVM(req, inventory); err != nil {
				return "", err
			}
		}
		got, err = e.provider.Stop(ctx, provider.StopRequest{Ref: req.Ref, OperationID: op.ID})
	}
	if err != nil {
		return "", err
	}
	return e.record(ctx, op, req, got)
}

func (e *PowerExecutor) request(ctx context.Context, op operation.Operation, step operation.Step) (provider.CreateRequest, error) {
	offboarding := op.OriginatingAction == "user.disable" && op.Type == "stop" && step.Key == "stop"
	if (!offboarding && !isRepairRetry(op) && op.OriginatingAction != "workspace."+op.Type) ||
		op.WorkspaceID == nil || op.StartedAt == nil || op.Status != "running" || step.Status != "active" ||
		!validPowerStep(op.Type, step.Key) {
		return provider.CreateRequest{}, provider.ErrInvalidState
	}
	req := provider.CreateRequest{OperationID: op.ID, Ref: provider.WorkspaceRef{WorkspaceID: *op.WorkspaceID, Generation: op.Generation}}
	var policy string
	err := e.store.db.QueryRowContext(ctx, `SELECT w.installation_id,w.ownership_marker,COALESCE(w.provider_instance_id,''),
		w.platform_artifact_id,a.release_id,COALESCE(w.network_policy_override_json,w.network_policy_snapshot_json)
		FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		JOIN installations i ON i.id=w.installation_id JOIN users owner ON owner.id=w.owner_user_id
		JOIN users actor ON actor.id=$3 JOIN operations o ON o.id=$5 AND o.workspace_id=w.id AND o.generation=w.generation
		WHERE w.id=$1 AND w.generation=$2 AND ($6 OR w.provider_instance_id IS NOT NULL) AND w.observed_state<>'deleted'
		  AND i.state='active' AND o.status='running' AND o.originating_action=$7
		  AND (($6 AND owner.membership_state IN ('pending','disabled')) OR
		       (NOT $6 AND actor.membership_state='active' AND (actor.id=owner.id OR actor.role='administrator')))
		  AND (($4='stop' AND w.desired_state='stopped') OR
		       ($4 IN ('start','rebuild','repair') AND w.desired_state='running' AND owner.membership_state='active'
		        AND ($4='start' OR w.observed_state='running')
		        AND EXISTS (SELECT 1 FROM reservations r WHERE r.workspace_id=w.id
		                     AND r.cpu_memory_state='reserved' AND r.disk_state='reserved')))`,
		req.Ref.WorkspaceID, op.Generation, op.Actor, op.Type, op.ID, offboarding, op.OriginatingAction).
		Scan(&req.Ref.InstallationID, &req.Ref.OwnershipMarker, &req.Ref.InstanceID, &req.ArtifactID, &req.Release, &policy)
	if err != nil {
		return provider.CreateRequest{}, err
	}
	var network struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal([]byte(policy), &network) != nil || network.Mode == "" || (req.Ref.InstanceID == "" && !offboarding) {
		return provider.CreateRequest{}, provider.ErrInvalidState
	}
	req.NetworkMode = network.Mode
	return req, nil
}

func isRepairRetry(op operation.Operation) bool {
	return op.Type == "rebuild" && op.OriginatingAction == "workspace.repair"
}

func validPowerStep(action, step string) bool {
	if action == "repair" {
		return step == "repair_enter" || step == "verify_repair"
	}
	if action == "stop" {
		return step == "stop" || step == "shutdown"
	}
	if action == "start" && step == "start" {
		return true
	}
	return (action == "start" || action == "rebuild") && (step == "configure" || step == "verify_ide" || step == "finish_setup")
}

func (e *PowerExecutor) repairEvidence(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	var status, id string
	err := e.store.db.QueryRowContext(ctx, "SELECT status,container_id FROM guest_dispatches WHERE operation_id=$1", op.ID).Scan(&status, &id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if status == "failed" {
		return "", &operation.StepFailure{Detail: "The guest could not start the preloaded repair environment."}
	}
	if status != "succeeded" || id == "" {
		return "", errGuestConfigurationPending
	}
	if step.Key == "verify_repair" {
		if e.probeRepair == nil {
			return "", provider.ErrInvalidState
		}
		if err := e.probeRepair(ctx, *op.WorkspaceID, op.Generation); err != nil {
			return "", errors.Join(errIDEProbePending, err)
		}
		return "The authenticated tunnel returned the repair workbench.", nil
	}
	return "The guest started the preloaded repair environment.", nil
}

func (e *PowerExecutor) shutdownEvidence(ctx context.Context, op operation.Operation) (string, error) {
	var status, problem string
	err := e.store.db.QueryRowContext(ctx, "SELECT status,problem FROM guest_dispatches WHERE operation_id=$1", op.ID).
		Scan(&status, &problem)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	switch status {
	case "succeeded":
		return "The guest confirmed its development workload stopped.", nil
	case "failed":
		if problem == "guest.shutdown_timed_out" {
			return "", &operation.StepFailure{Detail: "The development workload did not stop within the guest deadline."}
		}
		return "", &operation.StepFailure{Detail: "The guest could not verify that the development workload stopped."}
	default:
		return "", errGuestShutdownPending
	}
}

func (e *PowerExecutor) record(ctx context.Context, op operation.Operation, req provider.CreateRequest, got provider.Inventory) (string, error) {
	if err := verifyWorkspaceVM(req, got); err != nil {
		return "", err
	}
	now := e.now().UTC()
	want := provider.Stopped
	if op.Type == "start" {
		want = provider.Running
	}
	if got.Power != want || got.ObservedAt.Before(*op.StartedAt) || got.ObservedAt.After(now) {
		return "", provider.ErrOutcomeUnknown
	}
	ref := req.Ref
	err := database.Transaction(ctx, e.store.db, func(tx *sql.Tx) error {
		// Reconcile only the matching operation and VM. Repeated observations
		// leave the version alone once this power state is already recorded.
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET observed_state=$1,provider_instance_id=COALESCE(provider_instance_id,$7),
			updated_at=CASE WHEN observed_state<>$1 THEN $2 ELSE updated_at END,
			version=CASE WHEN observed_state<>$1 THEN version+1 ELSE version END
			WHERE id=$3 AND installation_id=$4 AND generation=$5 AND ownership_marker=$6
			  AND (provider_instance_id=$7 OR ($9 AND provider_instance_id IS NULL)) AND desired_state=$1 AND observed_state<>'deleted'
			  AND EXISTS (SELECT 1 FROM operations o WHERE o.id=$8 AND o.workspace_id=$3 AND o.generation=$5 AND o.status='running')`,
			string(want), got.ObservedAt.UTC().Format(time.RFC3339Nano), ref.WorkspaceID, ref.InstallationID, ref.Generation,
			ref.OwnershipMarker, ref.InstanceID, op.ID, op.OriginatingAction == "user.disable")
		if err := capacityWrite(result, err); err != nil {
			return err
		}
		if want == provider.Stopped {
			return ReleaseReservation(ctx, tx, got, now)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if want == provider.Stopped {
		return "Provider confirmed the VM stopped and the disk was retained.", nil
	}

	var last, expiry sql.NullString
	err = e.store.db.QueryRowContext(ctx, `SELECT r.last_authenticated_at,r.certificate_not_after
		FROM guest_registrations r JOIN workspaces w ON w.id=r.workspace_id AND w.generation=r.generation
		WHERE w.id=$1 AND w.generation=$2 AND w.provider_instance_id=$3 AND w.desired_state='running' AND r.state='active'`,
		ref.WorkspaceID, ref.Generation, ref.InstanceID).Scan(&last, &expiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !expiry.Valid {
		return "", errGuestContactPending
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry.String)
	if err != nil {
		return "", err
	}
	if !now.Before(deadline) {
		return "", &operation.StepWaiting{Detail: "The guest certificate expired while the VM was running. The live VM has been preserved for administrator recovery."}
	}
	if !last.Valid {
		return "", errGuestContactPending
	}
	contact, err := time.Parse(time.RFC3339Nano, last.String)
	if err != nil {
		return "", err
	}
	if contact.Before(*op.StartedAt) || contact.After(now) {
		return "", errGuestContactPending
	}
	if e.recovery != nil && e.recovery.enrollment != nil {
		if err := e.recovery.enrollment.CleanupDelivery(ctx, req.OperationID, ref, e.recovery.mediaRoot); err != nil {
			return "", err
		}
	}
	return "Provider confirmed the VM running and the guest authenticated.", nil
}
