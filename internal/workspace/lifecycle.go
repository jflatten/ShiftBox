package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

var (
	// ErrPowerConflict indicates that the request key already identifies another intent.
	ErrPowerConflict = errors.New("workspace power intent conflict")
	// ErrVersionMismatch indicates a stale workspace precondition.
	ErrVersionMismatch = errors.New("workspace version mismatch")
	// ErrOperationInProgress indicates that the workspace has unsettled work.
	ErrOperationInProgress = errors.New("workspace operation in progress")
)

// PowerIntent identifies an authorized start, stop, or rebuild of a retained workspace.
type PowerIntent struct {
	WorkspaceID, ActorID, RequestKey, Action string
	Version                                  int64
}

// AdmitPower records the desired state, start reservation and operation in the
// caller's transaction. Provider state and stop accounting change during reconciliation.
func AdmitPower(ctx context.Context, tx *sql.Tx, journal *operation.Journal, intent PowerIntent, now time.Time) (operation.Operation, error) {
	var desired, label string
	switch intent.Action {
	case "start":
		desired, label = "running", "Start workspace"
	case "stop":
		desired, label = "stopped", "Stop workspace"
	case "rebuild":
		desired, label = "running", "Rebuild the development container"
	default:
		return operation.Operation{}, provider.ErrInvalidState
	}

	var installation string
	err := tx.QueryRowContext(ctx, `SELECT installation_id FROM workspaces WHERE id=$1`, intent.WorkspaceID).Scan(&installation)
	if err != nil {
		return operation.Operation{}, err
	}
	// Use the same admission lock as launch, since the request key is scoped
	// to the actor and can collide across workspaces and action types.
	result, err := tx.ExecContext(ctx, `UPDATE installations SET version=version WHERE id=$1 AND state='active'`, installation)
	if err := capacityWrite(result, err); err != nil {
		return operation.Operation{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2`, intent.ActorID, intent.RequestKey).Scan(&id)
	if err == nil {
		op, err := journal.GetInTx(ctx, tx, id)
		if err != nil {
			return operation.Operation{}, err
		}
		if op.WorkspaceID == nil || *op.WorkspaceID != intent.WorkspaceID || op.Type != intent.Action ||
			op.OriginatingAction != "workspace."+intent.Action {
			return operation.Operation{}, ErrPowerConflict
		}
		return op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, err
	}

	var generation, version int64
	var current, observed, instance string
	if err := tx.QueryRowContext(ctx, `SELECT generation,version,desired_state,observed_state,COALESCE(provider_instance_id,'')
		FROM workspaces WHERE id=$1`, intent.WorkspaceID).Scan(&generation, &version, &current, &observed, &instance); err != nil {
		return operation.Operation{}, err
	}
	if version != intent.Version {
		return operation.Operation{}, ErrVersionMismatch
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM operations WHERE workspace_id=$1 AND generation=$2 AND status IN ('pending','running','waiting'))`,
		intent.WorkspaceID, generation).Scan(&active); err != nil {
		return operation.Operation{}, err
	}
	if active {
		return operation.Operation{}, ErrOperationInProgress
	}
	if current == "deleted" || observed == "deleted" || instance == "" {
		return operation.Operation{}, provider.ErrInvalidState
	}
	if intent.Action == "rebuild" && (current != "running" || observed != "running") {
		return operation.Operation{}, provider.ErrInvalidState
	}
	result, err = tx.ExecContext(ctx, `UPDATE workspaces SET desired_state=$2,updated_at=$3,version=version+1
		WHERE id=$1 AND version=$4`, intent.WorkspaceID, desired, now.UTC().Format(time.RFC3339Nano), version)
	if err := capacityWrite(result, err); err != nil {
		if errors.Is(err, ErrCapacityConflict) {
			return operation.Operation{}, ErrVersionMismatch
		}
		return operation.Operation{}, err
	}
	if intent.Action == "start" {
		if err := ReserveStart(ctx, tx, installation, intent.WorkspaceID, generation, now); err != nil {
			return operation.Operation{}, err
		}
	}
	var steps []operation.Step
	switch intent.Action {
	case "stop":
		steps = []operation.Step{{Key: "shutdown", Label: "Stop workspace workloads"}, {Key: "stop", Label: label}}
	case "start":
		steps = append([]operation.Step{{Key: "start", Label: label}}, developmentSteps("Start the development container")...)
	case "rebuild":
		steps = developmentSteps(label)
	}
	return journal.CreateInTx(ctx, tx, operation.Request{
		Actor: intent.ActorID, Key: intent.RequestKey, Type: intent.Action, OriginatingAction: "workspace." + intent.Action,
		WorkspaceID: &intent.WorkspaceID, Generation: generation, Steps: steps,
	})
}

func developmentSteps(label string) []operation.Step {
	return []operation.Step{
		{Key: "configure", Label: label},
		{Key: "verify_ide", Label: "Verify the browser IDE"},
		{Key: "finish_setup", Label: "Finish remaining setup hooks"},
	}
}
