package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

func (e *PowerExecutor) start(ctx context.Context, op operation.Operation, step operation.Step, req provider.CreateRequest) (provider.Inventory, error) {
	got, err := e.provider.Inspect(ctx, req.Ref)
	if err != nil {
		return got, err
	}
	if err := verifyWorkspaceVM(req, got); err != nil {
		return got, err
	}
	if got.Power == provider.Running {
		return got, nil
	}
	now := e.now().UTC()
	if got.Power != provider.Stopped || got.ObservedAt.Before(*op.StartedAt) || got.ObservedAt.After(now) {
		return got, provider.ErrOutcomeUnknown
	}
	requiresRecovery, err := e.needsEnrollment(ctx, req.Ref, now)
	if err != nil {
		return got, err
	}
	start := provider.StartRequest{Ref: req.Ref, OperationID: op.ID, NetworkMode: req.NetworkMode, RequiresEnrollment: requiresRecovery}
	if requiresRecovery {
		if e.recovery == nil || e.recovery.enrollment == nil {
			return got, &operation.StepWaiting{Detail: "The stopped guest needs fresh enrollment media. Check the controller's recovery configuration."}
		}
		start.EnrollmentMedia, err = e.recovery.enrollment.Deliver(ctx, op.ID, got, now,
			e.recovery.mediaRoot, e.recovery.endpoint, e.recovery.pollEndpoint, e.provider.DeliverEnrollment)
		if err != nil {
			return got, &operation.StepWaiting{Detail: "ShiftBox could not deliver fresh enrollment media to the stopped guest. Check the VM's removable drive and controller enrollment service.", Cause: err}
		}
	}
	// Recheck authorization after the external effect. Revocation may have
	// cancelled the start while the controller was delivering the media.
	current, err := e.request(ctx, op, step)
	if err != nil {
		return got, err
	}
	if current != req {
		return got, provider.ErrOwnershipMismatch
	}
	stillRequires, err := e.needsEnrollment(ctx, req.Ref, e.now().UTC())
	if err != nil {
		return got, err
	}
	if stillRequires != start.RequiresEnrollment {
		return got, provider.ErrOutcomeUnknown
	}
	return e.provider.Start(ctx, start)
}

// needsEnrollment transitions only the current expired leaf on the verified
// stopped VM. The enrollment service separately checks grant authority.
func (e *PowerExecutor) needsEnrollment(ctx context.Context, ref provider.WorkspaceRef, now time.Time) (bool, error) {
	var state, expiry string
	err := e.store.db.QueryRowContext(ctx, `SELECT r.state,COALESCE(r.certificate_not_after,'')
		FROM guest_registrations r JOIN workspaces w ON w.id=r.workspace_id AND w.generation=r.generation
		WHERE w.id=$1 AND w.generation=$2 AND w.installation_id=$3 AND w.ownership_marker=$4
		AND w.provider_instance_id=$5 AND w.desired_state='running' AND w.deletion_commit_at IS NULL`,
		ref.WorkspaceID, ref.Generation, ref.InstallationID, ref.OwnershipMarker, ref.InstanceID).Scan(&state, &expiry)
	if errors.Is(err, sql.ErrNoRows) || err == nil && state != "active" && state != "expired" {
		return false, &operation.StepFailure{Detail: "The retained guest identity is ineligible for automatic recovery. Ask an administrator to verify the workspace."}
	}
	if err != nil || state == "expired" {
		return state == "expired", err
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return false, &operation.StepFailure{Detail: "ShiftBox could not verify the retained guest's certificate expiration."}
	}
	if now.Before(deadline) {
		return false, nil
	}
	result, err := e.store.db.ExecContext(ctx, `UPDATE guest_registrations SET state='expired'
		WHERE workspace_id=$1 AND generation=$2 AND state='active' AND certificate_not_after=$3`,
		ref.WorkspaceID, ref.Generation, expiry)
	if err := capacityWrite(result, err); err != nil {
		return false, err
	}
	return true, nil
}
