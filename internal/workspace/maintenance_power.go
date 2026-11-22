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
	"github.com/jflatten/ShiftBox/internal/release"
)

type maintenancePower struct {
	machines provider.Provider
	now      func() time.Time
	recovery *LaunchExecutor
}

func (e maintenanceUpdateExecutor) powerRequest(ctx context.Context, op operation.Operation, step operation.Step) (provider.CreateRequest, error) {
	req := provider.CreateRequest{OperationID: op.ID, Ref: provider.WorkspaceRef{
		InstallationID: e.installation, WorkspaceID: *op.WorkspaceID, Generation: op.Generation}}
	var network struct{ Mode string }
	var raw string
	err := e.store.db.QueryRowContext(ctx, `SELECT w.provider_instance_id,w.ownership_marker,a.id,a.release_id,
		COALESCE(w.network_policy_override_json,w.network_policy_snapshot_json)
		FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
		JOIN operations o ON o.id=t.operation_id AND o.generation=t.generation AND o.workspace_id=t.workspace_id
		JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation AND w.installation_id=c.installation_id
		JOIN installations i ON i.id=w.installation_id JOIN users u ON u.id=w.owner_user_id
		JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		WHERE t.operation_id=$1 AND w.id=$2 AND w.generation=$3 AND w.installation_id=$4
		AND o.status='running' AND o.current_step=$5 AND o.originating_action='maintenance.update_guest'
		AND c.state='active' AND i.state='active' AND u.membership_state='active'
		AND t.prior_desired_state='stopped' AND w.desired_state='stopped' AND w.observed_state IN ('stopped','running')
		AND w.deletion_commit_at IS NULL AND w.provider_instance_id IS NOT NULL`,
		op.ID, req.Ref.WorkspaceID, req.Ref.Generation, req.Ref.InstallationID, step.Key).
		Scan(&req.Ref.InstanceID, &req.Ref.OwnershipMarker, &req.ArtifactID, &req.Release, &raw)
	if err != nil {
		return req, err
	}
	if json.Unmarshal([]byte(raw), &network) != nil || network.Mode == "" || req.Ref.InstanceID == "" {
		return req, provider.ErrInvalidState
	}
	req.NetworkMode = network.Mode
	return req, nil
}

func (e maintenanceUpdateExecutor) reconcilePower(ctx context.Context, op operation.Operation, step operation.Step, apply bool) (string, bool, error) {
	if e.power == nil {
		return "", false, &operation.StepWaiting{Detail: "The maintenance power executor is unavailable."}
	}
	req, err := e.powerRequest(ctx, op, step)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, &operation.StepFailure{Detail: "The workspace is no longer eligible for a management boot or shutdown."}
	}
	if err != nil {
		return "", false, err
	}
	got, err := e.power.machines.Inspect(ctx, req.Ref)
	if err != nil {
		return "", false, err
	}
	if err := verifyWorkspaceVM(req, got); err != nil {
		return "", false, err
	}
	now := e.power.now()
	if op.StartedAt == nil || got.ObservedAt.Before(*op.StartedAt) || got.ObservedAt.After(now) ||
		(got.Power != provider.Running && got.Power != provider.Stopped) {
		return "", false, provider.ErrOutcomeUnknown
	}
	want := provider.Stopped
	if step.Key == "maintenance_boot" {
		want = provider.Running
		if got.Power == provider.Stopped {
			if !apply {
				return "", false, nil
			}
			if err := e.checkManagementBootRelease(ctx, op.ID); err != nil {
				return "", false, err
			}
		}
		if got.Power == provider.Stopped && e.power.recovery != nil {
			if _, err := e.store.needsEnrollment(ctx, req.Ref, "stopped", now); err != nil {
				return "", false, err
			}
		}
		if err := database.Transaction(ctx, e.store.db, func(tx *sql.Tx) error {
			return reserveStart(ctx, tx, e.installation, req.Ref.WorkspaceID, req.Ref.Generation, "stopped", now)
		}); err != nil {
			return "", false, &operation.StepWaiting{Detail: "Waiting for capacity to boot the workspace for maintenance.", Cause: err}
		}
	}
	if got.Power != want {
		if !apply {
			return "", false, nil
		}
		if current, err := e.powerRequest(ctx, op, step); err != nil || current != req {
			return "", false, provider.ErrInvalidState
		}
		switch {
		case want == provider.Running && got.Power == provider.Stopped:
			got, err = e.startManagementGuest(ctx, op, step, req, got, now)
		case want == provider.Stopped && got.Power == provider.Running:
			got, err = e.power.machines.Stop(ctx, provider.StopRequest{Ref: req.Ref, OperationID: op.ID})
		default:
			return "", false, provider.ErrOutcomeUnknown
		}
		if err != nil {
			return "", false, err
		}
	}
	now = e.power.now()
	if err := verifyWorkspaceVM(req, got); err != nil {
		return "", false, err
	}
	if got.Power != want || got.ObservedAt.Before(*op.StartedAt) || got.ObservedAt.After(now) {
		return "", false, provider.ErrOutcomeUnknown
	}
	err = database.Transaction(ctx, e.store.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET observed_state=$1,
			updated_at=CASE WHEN observed_state<>$1 THEN $8 ELSE updated_at END,
			version=CASE WHEN observed_state<>$1 THEN version+1 ELSE version END
			WHERE installation_id=$2 AND id=$3 AND generation=$4 AND provider_instance_id=$5
			AND desired_state='stopped' AND deletion_commit_at IS NULL AND EXISTS (
			SELECT 1 FROM operations o WHERE o.id=$6 AND o.status='running' AND o.current_step=$7)`,
			string(want), e.installation, req.Ref.WorkspaceID, req.Ref.Generation, req.Ref.InstanceID, op.ID, step.Key, got.ObservedAt.UTC().Format(time.RFC3339Nano))
		if err := capacityWrite(result, err); err != nil {
			return err
		}
		if want == provider.Stopped {
			return ReleaseReservation(ctx, tx, got, now)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if want == provider.Stopped {
		return "The provider confirmed the workspace stopped and its disk was retained.", true, nil
	}
	if e.power.recovery != nil {
		var contact string
		err := e.store.db.QueryRowContext(ctx, `SELECT COALESCE(last_authenticated_at,'') FROM guest_registrations
			WHERE workspace_id=$1 AND generation=$2 AND state='active'`, req.Ref.WorkspaceID, req.Ref.Generation).Scan(&contact)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		stamp, err := time.Parse(time.RFC3339Nano, contact)
		if err != nil || stamp.Before(*op.StartedAt) || stamp.After(now) {
			return "", false, &operation.StepWaiting{Detail: "Waiting for the maintenance guest to authenticate."}
		}
		if err := e.power.recovery.enrollment.CleanupDelivery(ctx, op.ID, req.Ref, e.power.recovery.mediaRoot); err != nil {
			return "", false, err
		}
	}
	return "The provider confirmed the management boot.", true, nil
}

func (e maintenanceUpdateExecutor) checkManagementBootRelease(ctx context.Context, operationID string) error {
	var product string
	err := e.store.db.QueryRowContext(ctx, `SELECT a.guest_release
		FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
		JOIN installations i ON i.id=c.installation_id
		JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation
		JOIN platform_artifacts source ON source.id=w.platform_artifact_id
		JOIN platform_artifacts a ON a.release_id=c.release_id AND a.state='available' AND a.guest_release=i.active_release
			AND a.architecture=source.architecture
		WHERE t.operation_id=$1 AND i.id=$2 AND i.state='active' AND c.state='active'`, operationID, e.installation).Scan(&product)
	if errors.Is(err, sql.ErrNoRows) {
		err = release.ErrGuestUpdateUnavailable
	}
	if err == nil {
		_, err = e.loadPinnedGuestUpdate(ctx, product)
	}
	if errors.Is(err, release.ErrGuestUpdateUnavailable) {
		return &operation.StepWaiting{Detail: "Restore the pinned guest release files before booting the workspace for maintenance.", Cause: err}
	}
	return err
}

func (e maintenanceUpdateExecutor) startManagementGuest(ctx context.Context, op operation.Operation, step operation.Step,
	req provider.CreateRequest, stopped provider.Inventory, now time.Time,
) (provider.Inventory, error) {
	start := provider.StartRequest{Ref: req.Ref, OperationID: op.ID, NetworkMode: req.NetworkMode}
	recovery := e.power.recovery
	if recovery != nil {
		var err error
		start.RequiresEnrollment, err = e.store.needsEnrollment(ctx, req.Ref, "stopped", now)
		if err != nil {
			return stopped, err
		}
		if start.RequiresEnrollment {
			start.EnrollmentMedia, err = recovery.enrollment.Deliver(ctx, op.ID, stopped, now, recovery.mediaRoot,
				recovery.endpoint, recovery.pollEndpoint, e.power.machines.DeliverEnrollment)
			if err != nil {
				return stopped, &operation.StepWaiting{Detail: "Waiting for the provider to attach fresh maintenance enrollment media.", Cause: err}
			}
		}
		if current, err := e.powerRequest(ctx, op, step); err != nil || current != req {
			return stopped, provider.ErrInvalidState
		}
		stillRequires, err := e.store.needsEnrollment(ctx, req.Ref, "stopped", e.power.now())
		if err != nil {
			return stopped, err
		}
		if stillRequires != start.RequiresEnrollment {
			return stopped, provider.ErrOutcomeUnknown
		}
	}
	return e.power.machines.Start(ctx, start)
}
