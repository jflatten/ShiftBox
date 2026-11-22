package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
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

// PowerIntent identifies an authorized lifecycle action for a retained workspace.
type PowerIntent struct {
	WorkspaceID, ActorID, RequestKey, Action string
	Version                                  int64
}

// SSHStart identifies a start that may be joined or admitted for a retained
// workspace, or a terminal status for an already-admitted wait.
type SSHStart struct {
	OperationID    string
	TerminalStatus string
	Version        int64
	CanStart       bool
}

const activePowerWork = `EXISTS (
	SELECT 1 FROM operations WHERE workspace_id=w.id AND generation=w.generation
		AND status IN ('pending','running','waiting'))
	OR EXISTS (` + dueMaintenanceTarget + ` AND c.deadline<=$2)`

const deploymentStopAction = "workspace.stop_deployment"

// InterruptibleDeployment identifies the sole active deployment that a
// workspace stop can supersede. Maintenance and deletion remain independent.
func InterruptibleDeployment(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var deployment string
	err := tx.QueryRowContext(ctx, `SELECT o.id FROM operations o JOIN workspaces w ON w.id=o.workspace_id
		WHERE w.id=$1 AND w.desired_state='running' AND w.observed_state<>'deleted'
		  AND o.generation=w.generation AND o.status IN ('pending','running','waiting')
		  AND (o.type='provision' AND o.originating_action='workspace.launch'
		    OR o.type='start' AND o.originating_action='workspace.start'
		    OR o.type='rebuild' AND o.originating_action IN ('workspace.rebuild','workspace.repair'))
		  AND NOT EXISTS (SELECT 1 FROM operations other WHERE other.workspace_id=w.id
		    AND other.generation=w.generation AND other.id<>o.id AND other.status IN ('pending','running','waiting'))`,
		id).Scan(&deployment)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return deployment, err
}

// ReadSSHStart applies the same competing-work check as AdmitPower and
// reads a bound wait's terminal status in the same snapshot as joinability.
func ReadSSHStart(ctx context.Context, tx *sql.Tx, id string, generation int64, waitingStartID string, now time.Time) (SSHStart, error) {
	var start SSHStart
	err := tx.QueryRowContext(ctx, `SELECT w.version,
		(w.provider_instance_id IS NOT NULL AND w.desired_state='stopped' AND w.observed_state='stopped' AND NOT (`+activePowerWork+`)),
		COALESCE((SELECT o.id FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
			AND o.type='start' AND o.originating_action='workspace.start' AND o.status IN ('pending','running','waiting')
			AND w.provider_instance_id IS NOT NULL AND w.desired_state='running'
			AND w.observed_state IN ('stopped','starting','running','unreachable')
			ORDER BY o.created_at DESC,o.id DESC LIMIT 1),''),
		COALESCE((SELECT o.status FROM operations o WHERE $4<>'' AND o.id=$4 AND o.workspace_id=w.id AND o.generation=w.generation
			AND o.type='start' AND o.originating_action='workspace.start' AND o.status IN ('failed','cancelled')
			AND w.desired_state<>'deleted' AND w.observed_state<>'deleted'),'')
		FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN users owner ON owner.id=w.owner_user_id
		WHERE w.id=$1 AND w.generation=$3 AND i.state='active' AND owner.membership_state='active'
			AND w.deletion_commit_at IS NULL AND (w.provider_instance_id IS NOT NULL OR $4<>'')`,
		id, now.UTC().Format(maintenanceTime), generation, waitingStartID).
		Scan(&start.Version, &start.CanStart, &start.OperationID, &start.TerminalStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return SSHStart{}, nil
	}
	return start, err
}

// LockPowerAdmission serializes a start-or-join decision with ordinary
// lifecycle admission in the caller's transaction.
func LockPowerAdmission(ctx context.Context, tx *sql.Tx, installation string) error {
	result, err := tx.ExecContext(ctx, `UPDATE installations SET version=version WHERE id=$1 AND state='active'`, installation)
	return capacityWrite(result, err)
}

// AdmitPower records the desired state, start reservation and operation in the
// caller's transaction. Provider state and stop accounting change during reconciliation.
func AdmitPower(ctx context.Context, tx *sql.Tx, journal *operation.Journal, intent PowerIntent, now time.Time) (operation.Operation, error) {
	var desired, label string
	kind, action := intent.Action, "workspace."+intent.Action
	switch intent.Action {
	case "start":
		desired, label = "running", "Start workspace"
	case "stop":
		desired, label = "stopped", "Stop workspace"
	case "delete":
		desired = "deleted"
	case "update":
		action = maintenanceTargetAction
	case "rebuild":
		desired, label = "running", "Rebuild the development container"
	case "repair":
		desired, label = "running", "Start the repair environment"
	case "retry":
		desired, label = "running", "Exit repair and retry development setup"
		kind, action = "rebuild", "workspace.repair"
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
	if err := LockPowerAdmission(ctx, tx, installation); err != nil {
		return operation.Operation{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2`, intent.ActorID, intent.RequestKey).Scan(&id)
	if err == nil {
		op, err := journal.GetInTx(ctx, tx, id)
		if err != nil {
			return operation.Operation{}, err
		}
		actionMatches := op.OriginatingAction == action || intent.Action == "stop" && op.OriginatingAction == deploymentStopAction
		if op.WorkspaceID == nil || *op.WorkspaceID != intent.WorkspaceID || op.Type != kind || !actionMatches {
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
	if intent.Action == "update" {
		return requestMaintenanceTarget(ctx, tx, journal, installation, intent, now)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT `+activePowerWork+` FROM workspaces w WHERE w.id=$1`,
		intent.WorkspaceID, now.UTC().Format(maintenanceTime)).Scan(&active); err != nil {
		return operation.Operation{}, err
	}
	var deployment string
	if active && intent.Action == "stop" {
		deployment, err = InterruptibleDeployment(ctx, tx, intent.WorkspaceID)
		if err != nil {
			return operation.Operation{}, err
		}
	}
	if active && deployment == "" {
		return operation.Operation{}, ErrOperationInProgress
	}
	var unprovisioned bool
	if instance == "" && intent.Action == "delete" && observed == "stopped" {
		if err := tx.QueryRowContext(ctx, `SELECT `+unprovisionedWorkspace+` FROM workspaces w WHERE w.id=$1`,
			intent.WorkspaceID).Scan(&unprovisioned); err != nil {
			return operation.Operation{}, err
		}
	}
	if current == "deleted" || observed == "deleted" || instance == "" && deployment == "" && !unprovisioned {
		return operation.Operation{}, provider.ErrInvalidState
	}
	if desired == "running" && intent.Action != "start" && (current != "running" || observed != "running") {
		return operation.Operation{}, provider.ErrInvalidState
	}
	if intent.Action == "repair" {
		eligible, err := RepairAvailable(ctx, tx, intent.WorkspaceID, generation, now)
		if err != nil {
			return operation.Operation{}, err
		}
		if !eligible {
			return operation.Operation{}, provider.ErrInvalidState
		}
	}
	if intent.Action == "retry" || intent.Action == "rebuild" {
		ready, err := RepairActive(ctx, tx, intent.WorkspaceID, generation)
		if err != nil {
			return operation.Operation{}, err
		}
		if ready != (intent.Action == "retry") {
			return operation.Operation{}, provider.ErrInvalidState
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET desired_state=$2,updated_at=$3,version=version+1,
		last_activity_at=CASE WHEN $5 THEN $3 ELSE last_activity_at END,
		deletion_commit_at=CASE WHEN $2='deleted' THEN $3 ELSE deletion_commit_at END
		WHERE id=$1 AND version=$4`, intent.WorkspaceID, desired, now.UTC().Format(time.RFC3339Nano), version, intent.Action == "start")
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
		if deployment == "" {
			steps = []operation.Step{{Key: "shutdown", Label: "Stop workspace workloads"}, {Key: "stop", Label: label}}
		} else {
			if err := journal.CancelInTx(ctx, tx, deployment, "Cancelled by a workspace shutdown request.", now); err != nil {
				return operation.Operation{}, err
			}
			// The guest may be stuck in setup or may not be enrolled yet.
			// Use the provider's normal graceful shutdown for this handoff.
			action = deploymentStopAction
			steps = []operation.Step{{Key: "stop", Label: label}}
		}
	case "delete":
		steps = deletionSteps()
		if unprovisioned {
			steps = steps[len(steps)-1:]
		}
		if err := revokeDeletionGrants(ctx, tx, intent.WorkspaceID, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return operation.Operation{}, err
		}
		if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: intent.ActorID,
			Action: action, ResourceType: "workspace", ResourceID: intent.WorkspaceID, Outcome: "allowed", ReasonCode: "confirmed_data_loss"}); err != nil {
			return operation.Operation{}, err
		}
	case "start":
		steps = append([]operation.Step{{Key: "start", Label: label}}, developmentSteps("Start the development container")...)
	case "rebuild", "retry":
		steps = developmentSteps(label)
	case "repair":
		steps = []operation.Step{{Key: "repair_enter", Label: label}, {Key: "verify_repair", Label: "Verify the repair IDE"}}
	}
	return journal.CreateInTx(ctx, tx, operation.Request{
		Actor: intent.ActorID, Key: intent.RequestKey, Type: kind, OriginatingAction: action,
		WorkspaceID: &intent.WorkspaceID, Generation: generation, Steps: steps,
	})
}

// RepairActive reports whether the latest workspace lifecycle verified repair.
func RepairActive(ctx context.Context, tx *sql.Tx, id string, generation int64) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM operations WHERE id=(SELECT id FROM operations WHERE workspace_id=$1 AND generation=$2
			AND type IN ('provision','start','stop','rebuild','repair','delete') ORDER BY created_at DESC,id DESC LIMIT 1)
		AND type='repair' AND originating_action='workspace.repair' AND status='succeeded'
	)`, id, generation).Scan(&active)
	return active, err
}

// RepairAvailable checks for a failed setup step and recent authenticated guest contact.
func RepairAvailable(ctx context.Context, tx *sql.Tx, id string, generation int64, now time.Time) (bool, error) {
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM operations o JOIN operation_steps s ON s.operation_id=o.id AND s.step_key=o.current_step
			JOIN guest_registrations g ON g.workspace_id=o.workspace_id AND g.generation=o.generation
			WHERE o.id=(SELECT id FROM operations WHERE workspace_id=$1 AND generation=$2
				AND type IN ('provision','start','rebuild','repair') ORDER BY created_at DESC,id DESC LIMIT 1)
			AND o.status='failed' AND s.step_key IN ('configure','verify_ide','repair_enter','verify_repair')
			AND g.state='active' AND g.last_authenticated_at >= $3 AND g.certificate_not_after > $4
		)`, id, generation, now.UTC().Add(-90*time.Second).Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano)).Scan(&eligible)
	return eligible, err
}

func developmentSteps(label string) []operation.Step {
	return []operation.Step{
		{Key: "configure", Label: label},
		{Key: "verify_ide", Label: "Verify the browser IDE"},
		{Key: "finish_setup", Label: "Finish remaining setup hooks"},
	}
}
