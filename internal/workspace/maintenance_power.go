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

type maintenancePower struct {
	machines provider.Provider
	now      func() time.Time
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
			got, err = e.power.machines.Start(ctx, provider.StartRequest{Ref: req.Ref, OperationID: op.ID, NetworkMode: req.NetworkMode})
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
	return "The provider confirmed the management boot.", true, nil
}
