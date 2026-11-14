package workspace

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/release"
)

// MaintenanceUpdateWorker advances the workspace's update operation using
// receipts written by the authenticated guest exchange.
func (s *Store) MaintenanceUpdateWorker(journal *operation.Journal, installation, protocol string,
	load func(context.Context, string) (release.GuestUpdate, error),
) *operation.Worker {
	return maintenanceTargetWorker(journal, maintenanceUpdateExecutor{store: s, installation: installation, protocol: protocol, load: load})
}

func maintenanceTargetWorker(journal *operation.Journal, executor maintenanceUpdateExecutor) *operation.Worker {
	return &operation.Worker{Journal: journal, Name: "maintenance-update", LeaseDuration: time.Minute,
		Actions: []string{maintenanceTargetAction}, Executor: executor}
}

// MaintenanceWorkers binds the campaign and target workers to the same
// enrollment and provider used for ordinary workspace starts.
func (s *Store) MaintenanceWorkers(journal *operation.Journal, installation, protocol string,
	load func(context.Context, string) (release.GuestUpdate, error), launch *LaunchExecutor,
) (*operation.Worker, *operation.Worker) {
	campaign := s.MaintenanceCampaignWorker(journal, installation, launch.now)
	target := maintenanceTargetWorker(journal, maintenanceUpdateExecutor{store: s, installation: installation, protocol: protocol,
		load: load, power: &maintenancePower{machines: launch.provision.provider, now: launch.now, recovery: launch}})
	return campaign, target
}

type maintenanceUpdateExecutor struct {
	store                  *Store
	installation, protocol string
	load                   func(context.Context, string) (release.GuestUpdate, error)
	power                  *maintenancePower
}

func (e maintenanceUpdateExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	if op.WorkspaceID == nil || op.Type != "update" {
		return "", false, &operation.StepFailure{Detail: "The workspace update operation is invalid."}
	}
	if step.Key == "maintenance_boot" || step.Key == "maintenance_restore" {
		return e.reconcilePower(ctx, op, step, false)
	}
	var product, staged, digest, activation string
	var reportedRelease, reportedProtocol string
	err := e.store.db.QueryRowContext(ctx, `SELECT a.guest_release,COALESCE(g.status,''),COALESCE(g.update_digest,''),
		COALESCE(g.activation_status,''),COALESCE(w.guest_release,''),COALESCE(w.protocol_version,'')
		FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
		JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation AND w.installation_id=c.installation_id
		JOIN installations i ON i.id=c.installation_id
		JOIN platform_artifacts a ON a.release_id=c.release_id AND a.state='available' AND a.guest_release=i.active_release
		LEFT JOIN guest_dispatches g ON g.operation_id=t.operation_id
		WHERE t.operation_id=$1 AND w.id=$2 AND w.generation=$3 AND i.id=$4 AND i.state='active'
		AND c.state='active' AND w.deletion_commit_at IS NULL`, op.ID, *op.WorkspaceID, op.Generation, e.installation).
		Scan(&product, &staged, &digest, &activation, &reportedRelease, &reportedProtocol)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, &operation.StepFailure{Detail: "The workspace or release is no longer eligible for this maintenance update."}
	}
	if err != nil {
		return "", false, err
	}
	switch step.Key {
	case "maintenance_stage":
		switch staged {
		case "failed":
			return "", false, maintenanceUpdateFailure(op, "The guest could not stage the signed update. Check the release files and the guest's publisher key.")
		case "succeeded":
			update, err := e.loadPinnedGuestUpdate(ctx, product)
			if err != nil {
				return "", false, &operation.StepWaiting{Detail: "Restore the pinned guest release files before continuing maintenance.", Cause: err}
			}
			if digest != hex.EncodeToString(update.Metadata.Digest[:]) {
				return "", false, maintenanceUpdateFailure(op, "The guest staged a different binary from the pinned release.")
			}
			return "The authenticated guest staged the pinned binary.", true, nil
		}
	case "maintenance_activate":
		switch activation {
		case "failed":
			return "", false, maintenanceUpdateFailure(op, "The guest rolled back the update. Review the release before retrying.")
		case "succeeded":
			return "The guest confirmed the supervisor trial.", true, nil
		}
	case "maintenance_verify":
		if activation == "succeeded" && reportedRelease == product && reportedProtocol == e.protocol {
			return "The authenticated guest reported the assigned release and protocol.", true, nil
		}
	}
	return "", false, nil
}

func (e maintenanceUpdateExecutor) loadPinnedGuestUpdate(ctx context.Context, product string) (release.GuestUpdate, error) {
	if e.load == nil {
		return release.GuestUpdate{}, release.ErrGuestUpdateUnavailable
	}
	update, err := e.load(ctx, product)
	if err != nil {
		return release.GuestUpdate{}, err
	}
	if update.Metadata.Release != product || update.Metadata.Protocol != e.protocol {
		return release.GuestUpdate{}, release.ErrGuestUpdateUnavailable
	}
	return update, nil
}

func maintenanceUpdateFailure(op operation.Operation, detail string) *operation.StepFailure {
	failure := &operation.StepFailure{Detail: detail}
	for _, step := range op.Steps {
		if step.Key == "maintenance_restore" && step.Status == "pending" {
			failure.CleanupStep = step.Key
			break
		}
	}
	return failure
}

func (e maintenanceUpdateExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	switch step.Key {
	case "maintenance_boot", "maintenance_restore":
		evidence, _, err := e.reconcilePower(ctx, op, step, true)
		return evidence, err
	case "maintenance_stage":
		return "", &operation.StepWaiting{Detail: "Waiting for the guest to verify and stage the signed update."}
	case "maintenance_activate":
		return "", &operation.StepWaiting{Detail: "Waiting for the guest to confirm the update trial or report rollback."}
	case "maintenance_verify":
		return "", &operation.StepWaiting{Detail: "Waiting for authenticated contact from the updated release."}
	default:
		return "", &operation.StepWaiting{Detail: "Waiting for the maintenance power step."}
	}
}
