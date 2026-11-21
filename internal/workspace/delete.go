package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

const retentionDeleteAction = "workspace.retention_delete"

// CommitRetentionDeletion fences the final policy check with the workspace's
// deletion intent, audit record and durable operation. Provider effects belong
// to the subsequent worker, after this transaction commits.
func (s *Store) CommitRetentionDeletion(ctx context.Context, journal *operation.Journal, candidate RetentionCandidate,
	now time.Time, policy RetentionEligibility,
) (operation.Operation, error) {
	if journal == nil || !candidate.verified || !policy.ready(now) {
		return operation.Operation{}, sql.ErrNoRows
	}
	var committed operation.Operation
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		ref := candidate.Ref
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1", ref.InstallationID); err != nil {
			return err
		}
		current, err := recheckRetention(ctx, tx, candidate, now, policy)
		if err != nil {
			return err
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET desired_state='deleted',deletion_commit_at=$1,
			retention_deadline=$2,updated_at=$1,version=version+1
			WHERE id=$3 AND generation=$4 AND version=$5 AND deletion_commit_at IS NULL
			AND COALESCE(last_activity_at,created_at)=$6 AND retention_seconds=$7`,
			stamp, current.Deadline.UTC().Format(time.RFC3339Nano), ref.WorkspaceID, ref.Generation, current.version,
			current.baseline, current.seconds)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return sql.ErrNoRows
		}
		if err := revokeDeletionGrants(ctx, tx, ref.WorkspaceID, stamp); err != nil {
			return err
		}
		committed, err = journal.CreateInTx(ctx, tx, operation.Request{
			Actor: "controller", Key: "retention-delete:" + ref.WorkspaceID + ":" + strconv.FormatInt(ref.Generation, 10),
			Type: "delete", OriginatingAction: retentionDeleteAction, WorkspaceID: &ref.WorkspaceID, Generation: ref.Generation,
			Steps: deletionSteps(),
		})
		if err != nil {
			return err
		}
		if committed.WorkspaceID == nil || *committed.WorkspaceID != ref.WorkspaceID || committed.Generation != ref.Generation ||
			committed.Type != "delete" || committed.OriginatingAction != retentionDeleteAction || committed.Status != "pending" {
			return operation.ErrConflict
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "controller", ActorID: ref.InstallationID,
			Action: retentionDeleteAction, ResourceType: "workspace", ResourceID: ref.WorkspaceID, Outcome: "allowed",
			ReasonCode: "retention_deadline_elapsed"})
		return err
	})
	if err != nil {
		return operation.Operation{}, err
	}
	return committed, nil
}

// revokeDeletionGrants retires outstanding access in the deletion-intent
// transaction. An already enrolled guest may still acknowledge shutdown.
func revokeDeletionGrants(ctx context.Context, tx *sql.Tx, id, stamp string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE origin_grants SET revoked_at=$2 WHERE workspace_id=$1 AND revoked_at IS NULL`, id, stamp); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE enrollment_grants SET state='invalidated' WHERE workspace_id=$1 AND state='issued'`, id)
	return err
}

func deletionSteps() []operation.Step {
	return []operation.Step{
		{Key: "shutdown", Label: "Request graceful shutdown"},
		{Key: "stop", Label: "Verify workspace stopped"},
		{Key: "delete", Label: "Delete workspace and retained files"},
	}
}

type deletionExecutor struct {
	store       *Store
	machines    provider.Provider
	now         func() time.Time
	closeAccess func(string, bool) error
}

// Observe checks shutdown evidence and owned resources before replaying deletion.
func (e deletionExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	if op.OriginatingAction == "workspace.delete" && op.WorkspaceID != nil && step.Key == "delete" && len(op.Steps) == 1 {
		completed, err := recordUnprovisionedPower(ctx, e.store, op, "deleted", e.now())
		if err != nil || completed {
			return "Removed the record of the deployment that stopped before provisioning.", completed, err
		}
	}
	req, shutdownAt, err := e.request(ctx, op, step)
	if err != nil {
		return "", false, err
	}
	if e.closeAccess != nil {
		if err := e.closeAccess(req.Ref.WorkspaceID, step.Key == "shutdown"); err != nil {
			return "", false, &operation.StepWaiting{Detail: "Waiting for workspace connections to close before continuing deletion.", Cause: err}
		}
	}
	got, err := e.machines.Inspect(ctx, req.Ref)
	if step.Key == "delete" && errors.Is(err, provider.ErrNotFound) {
		// The adapter's idempotent Delete verifies its managed storage even
		// when the provider reports the VM is absent.
		return "", false, nil
	}
	if err != nil {
		return "", false, retentionWaiting(err)
	}
	if step.Key == "delete" && got.Power == provider.Deleted {
		return e.record(ctx, op, req.Ref, got, provider.Deleted)
	}
	if err := verifyWorkspaceVM(req, got); err != nil {
		return "", false, retentionWaiting(err)
	}
	switch step.Key {
	case "shutdown":
		if got.Power == provider.Stopped {
			return "Provider verified that the workspace is already stopped.", true, nil
		}
		if got.Power != provider.Running {
			return "", false, retentionWaiting(provider.ErrOutcomeUnknown)
		}
		var status string
		err := e.store.db.QueryRowContext(ctx, "SELECT status FROM guest_dispatches WHERE operation_id=$1", op.ID).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if status == "succeeded" {
			return "Guest verified graceful workload shutdown.", true, nil
		}
		if !e.now().Before(shutdownAt.Add(time.Minute)) {
			if op.OriginatingAction == retentionDeleteAction {
				return "The graceful shutdown window elapsed. Retention authorizes provider-enforced stop.", true, nil
			}
			return "The guest did not confirm shutdown. Request a graceful stop through the provider.", true, nil
		}
		return "", false, &operation.StepWaiting{Cause: errGuestShutdownPending, Detail: "Waiting for graceful workload shutdown before removing the workspace and retained files."}
	case "stop":
		if got.Power == provider.Stopped {
			return e.record(ctx, op, req.Ref, got, provider.Stopped)
		}
		if got.Power == provider.Running {
			return "", false, nil
		}
	case "delete":
		if got.Power == provider.Stopped {
			return "", false, nil
		}
	}
	return "", false, retentionWaiting(provider.ErrOutcomeUnknown)
}

// Apply rechecks deletion authority before stopping or removing the exact VM.
func (e deletionExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	req, shutdownAt, err := e.request(ctx, op, step)
	if err != nil {
		return "", err
	}
	var got provider.Inventory
	var want provider.PowerState
	switch step.Key {
	case "stop":
		want = provider.Stopped
		got, err = e.machines.Stop(ctx, provider.StopRequest{Ref: req.Ref, OperationID: op.ID,
			ForceRetentionDelete: op.OriginatingAction == retentionDeleteAction && !e.now().Before(shutdownAt.Add(time.Minute))})
	case "delete":
		want = provider.Deleted
		got, err = e.machines.Delete(ctx, provider.DeleteRequest{Ref: req.Ref, OperationID: op.ID})
	default:
		return "", provider.ErrInvalidState
	}
	if err != nil {
		return "", retentionWaiting(err)
	}
	if want == provider.Stopped {
		if err := verifyWorkspaceVM(req, got); err != nil {
			return "", retentionWaiting(err)
		}
	}
	evidence, _, err := e.record(ctx, op, req.Ref, got, want)
	return evidence, err
}

func (e deletionExecutor) request(ctx context.Context, op operation.Operation, step operation.Step) (provider.CreateRequest, time.Time, error) {
	authorizedAction := (op.OriginatingAction == retentionDeleteAction && op.Actor == "controller") ||
		(op.OriginatingAction == "workspace.delete" && op.Actor != "" && op.Actor != "controller")
	if op.Type != "delete" || !authorizedAction || op.WorkspaceID == nil ||
		op.StartedAt == nil || op.Status != "running" || step.Status != "active" {
		return provider.CreateRequest{}, time.Time{}, provider.ErrInvalidState
	}
	req := provider.CreateRequest{OperationID: op.ID, Ref: provider.WorkspaceRef{WorkspaceID: *op.WorkspaceID, Generation: op.Generation}}
	var started, network string
	err := e.store.db.QueryRowContext(ctx, `SELECT w.installation_id,w.provider_instance_id,w.ownership_marker,
		w.platform_artifact_id,a.release_id,COALESCE(w.network_policy_override_json,w.network_policy_snapshot_json),s.started_at
		FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		JOIN operations o ON o.workspace_id=w.id AND o.generation=w.generation
		JOIN operation_steps s ON s.operation_id=o.id AND s.step_key='shutdown'
		WHERE w.id=$1 AND w.generation=$2 AND o.id=$3 AND o.status='running' AND o.current_step=$4
		  AND w.desired_state='deleted' AND w.deletion_commit_at IS NOT NULL AND i.state='active'`,
		req.Ref.WorkspaceID, req.Ref.Generation, op.ID, step.Key).
		Scan(&req.Ref.InstallationID, &req.Ref.InstanceID, &req.Ref.OwnershipMarker, &req.ArtifactID, &req.Release, &network, &started)
	if err != nil {
		return req, time.Time{}, err
	}
	if req.Ref.InstanceID == "" {
		return req, time.Time{}, provider.ErrInvalidState
	}
	req.NetworkMode, err = effectiveNetworkMode(network)
	if err != nil {
		return req, time.Time{}, err
	}
	shutdownAt, err := time.Parse(time.RFC3339Nano, started)
	return req, shutdownAt, err
}

func (e deletionExecutor) record(ctx context.Context, op operation.Operation, ref provider.WorkspaceRef, got provider.Inventory, want provider.PowerState) (string, bool, error) {
	now := e.now().UTC()
	if got.Ref != ref || got.Power != want || got.ObservedAt.Before(*op.StartedAt) {
		return "", false, retentionWaiting(provider.ErrOutcomeUnknown)
	}
	err := database.Transaction(ctx, e.store.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET observed_state=$1,
			updated_at=CASE WHEN observed_state<>$1 THEN $2 ELSE updated_at END,
			version=CASE WHEN observed_state<>$1 THEN version+1 ELSE version END
			WHERE id=$3 AND generation=$4 AND desired_state='deleted' AND deletion_commit_at IS NOT NULL
			AND (observed_state<>'deleted' OR $1='deleted')
			AND EXISTS(SELECT 1 FROM operations WHERE id=$5 AND status='running' AND current_step=$6)`,
			string(want), now.Format(time.RFC3339Nano), ref.WorkspaceID, ref.Generation, op.ID, op.CurrentStep)
		if err := capacityWrite(result, err); err != nil {
			return err
		}
		if want == provider.Deleted {
			if _, err := tx.ExecContext(ctx, `UPDATE guest_registrations SET state='deleted',
				revoked_at=COALESCE(revoked_at,$3),revocation_reason=COALESCE(revocation_reason,'workspace_deleted')
				WHERE workspace_id=$1 AND generation=$2 AND state<>'deleted'`,
				ref.WorkspaceID, ref.Generation, now.Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return ReleaseReservation(ctx, tx, got, now)
	})
	if err != nil {
		return "", false, retentionWaiting(err)
	}
	if want == provider.Stopped {
		return "Provider verified the workspace stopped. Retained files remain reserved until removal is verified.", true, nil
	}
	return "Provider verified that the workspace and its owned resources were removed.", true, nil
}

func retentionWaiting(err error) error {
	classified := workspaceEffectError(operation.Operation{}, err)
	var waiting *operation.StepWaiting
	if errors.As(classified, &waiting) {
		return waiting
	}
	return &operation.StepWaiting{Cause: err, Detail: "Waiting for the provider to verify workspace cleanup. Unconfirmed resources remain reserved."}
}
