package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

var (
	// ErrMaintenanceInvalid indicates an invalid campaign intent.
	ErrMaintenanceInvalid = errors.New("invalid maintenance campaign")
	// ErrMaintenanceUnavailable indicates the release cannot be scheduled or work is already scheduled.
	ErrMaintenanceUnavailable = errors.New("maintenance campaign unavailable")
)

const maintenanceTime = "2006-01-02T15:04:05.000000000Z"
const maintenanceCampaignAction = "maintenance.manage"
const maintenanceTargetAction = "maintenance.update_guest"

// Conflicting work is fenced by the durable deadline even before a worker
// claims the target. A terminal target can be repaired or explicitly retired.
const dueMaintenanceTarget = `SELECT 1 FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
	WHERE t.workspace_id=w.id AND t.generation=w.generation AND c.installation_id=w.installation_id
	AND c.state IN ('scheduled','active') AND t.state NOT IN ('completed','failed')`

// MaintenanceCampaign is the public campaign status and journal reference.
type MaintenanceCampaign struct {
	ID          string    `json:"id"`
	ReleaseID   string    `json:"releaseId"`
	Deadline    time.Time `json:"deadline"`
	State       string    `json:"state"`
	OperationID string    `json:"operationId"`
}

// WorkspaceMaintenance describes the current campaign target for a workspace.
type WorkspaceMaintenance struct {
	ReleaseID   string    `json:"releaseId"`
	Deadline    time.Time `json:"deadline"`
	State       string    `json:"state"`
	OperationID *string   `json:"operationId"`
}

// ReadWorkspaceMaintenance returns a target only while its campaign and
// workspace generation still apply.
func ReadWorkspaceMaintenance(ctx context.Context, tx *sql.Tx, workspaceID string, generation int64) (*WorkspaceMaintenance, error) {
	var target WorkspaceMaintenance
	var deadline string
	err := tx.QueryRowContext(ctx, `SELECT c.release_id,c.deadline,t.state,t.operation_id
		FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
		JOIN workspaces w ON w.id=t.workspace_id AND w.installation_id=c.installation_id
		WHERE t.workspace_id=$1 AND t.generation=$2 AND w.generation=t.generation
		AND w.desired_state<>'deleted' AND w.observed_state<>'deleted'
		AND c.state IN ('scheduled','active','blocked') AND t.state<>'completed'
		ORDER BY c.created_at DESC,c.operation_id DESC LIMIT 1`, workspaceID, generation).
		Scan(&target.ReleaseID, &deadline, &target.State, &target.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	target.Deadline, err = time.Parse(maintenanceTime, deadline)
	if err != nil {
		return nil, err
	}
	return &target, nil
}

// ScheduleMaintenance records the campaign and its retained workspace targets
// in the authorization transaction. Scheduling itself performs no guest or VM effects.
func ScheduleMaintenance(ctx context.Context, tx *sql.Tx, journal *operation.Journal, installation, actor, key, releaseID string, deadline, now time.Time) (operation.Operation, error) {
	if releaseID == "" || len(releaseID) > 128 || deadline.IsZero() {
		return operation.Operation{}, ErrMaintenanceInvalid
	}
	result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state='active'", installation)
	if err := capacityWrite(result, err); err != nil {
		return operation.Operation{}, err
	}
	stamp := deadline.UTC().Format(maintenanceTime)
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", actor, key).Scan(&existing)
	if err == nil {
		var matches bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM maintenance_campaigns
			WHERE operation_id=$1 AND installation_id=$2 AND release_id=$3 AND deadline=$4)`,
			existing, installation, releaseID, stamp).Scan(&matches)
		if err != nil {
			return operation.Operation{}, err
		}
		if !matches {
			return operation.Operation{}, operation.ErrConflict
		}
		return journal.GetInTx(ctx, tx, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, err
	}
	if !deadline.After(now) {
		return operation.Operation{}, ErrMaintenanceInvalid
	}
	// A failed target can be included in an explicitly scheduled replacement.
	// If removal has already started, wait for proof before admitting a
	// campaign that would omit that failed workspace.
	var available bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM installations i JOIN platform_artifacts a ON a.release_id=$2 AND a.guest_release=i.active_release
		WHERE i.id=$1 AND a.state='available' AND NOT EXISTS (
			SELECT 1 FROM maintenance_campaigns c WHERE c.installation_id=i.id AND (
				c.state IN ('scheduled','active') OR (c.state='blocked' AND EXISTS (
					SELECT 1 FROM maintenance_targets t JOIN workspaces w ON w.id=t.workspace_id
					WHERE t.campaign_id=c.operation_id AND t.state='failed' AND w.installation_id=i.id
					AND w.generation=t.generation AND w.observed_state<>'deleted' AND w.deletion_commit_at IS NOT NULL)))
		))`, installation, releaseID).Scan(&available)
	if err != nil {
		return operation.Operation{}, err
	}
	if !available {
		return operation.Operation{}, ErrMaintenanceUnavailable
	}
	op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: actor, Key: key, Type: "update",
		OriginatingAction: maintenanceCampaignAction, Generation: 1,
		Steps: []operation.Step{{Key: "update_guests", Label: "Update retained workspaces"}}})
	if err != nil {
		return operation.Operation{}, err
	}
	created := now.UTC().Format(maintenanceTime)
	if _, err := tx.ExecContext(ctx, `INSERT INTO maintenance_campaigns(operation_id,installation_id,release_id,deadline,created_by,created_at,state)
		VALUES($1,$2,$3,$4,$5,$6,'scheduled')`, op.ID, installation, releaseID, stamp, actor, created); err != nil {
		return operation.Operation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO maintenance_targets(campaign_id,workspace_id,generation,prior_desired_state,prior_observed_state,state,updated_at)
		SELECT $1,id,generation,desired_state,observed_state,'pending',$3 FROM workspaces
		WHERE installation_id=$2 AND desired_state<>'deleted' AND observed_state<>'deleted' AND deletion_commit_at IS NULL`,
		op.ID, installation, created)
	return op, err
}

// MaintenanceCampaignWorker reconciles campaign deadlines and aggregates the
// durable outcomes of workspace update operations.
func (s *Store) MaintenanceCampaignWorker(journal *operation.Journal, installation string, now func() time.Time) *operation.Worker {
	return &operation.Worker{Journal: journal, Name: "maintenance-campaign", LeaseDuration: time.Minute,
		Actions: []string{maintenanceCampaignAction}, Executor: maintenanceCampaignExecutor{s, journal, installation, now}}
}

type maintenanceCampaignExecutor struct {
	store        *Store
	journal      *operation.Journal
	installation string
	now          func() time.Time
}

// Observe leaves campaign reconciliation to the transactional work in Apply.
func (e maintenanceCampaignExecutor) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	// Apply reconciles persisted campaign state without making external calls.
	return "", false, nil
}

// Apply reconciles durable target outcomes into the campaign's state.
func (e maintenanceCampaignExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if op.WorkspaceID != nil || step.Key != "update_guests" {
		return "", &operation.StepFailure{Detail: "The maintenance campaign operation is invalid."}
	}
	now := e.now()
	if err := e.store.PrepareMaintenanceTargets(ctx, e.journal, e.installation, op.ID, now); err != nil {
		return "", err
	}
	state, err := e.store.reconcileMaintenanceCampaign(ctx, e.installation, op.ID, now)
	if err != nil {
		return "", err
	}
	switch state {
	case "scheduled":
		return "", &operation.StepWaiting{Detail: "Waiting for the maintenance deadline."}
	case "active":
		return "", &operation.StepWaiting{Detail: "Waiting for safe workspace updates to finish."}
	case "blocked":
		return "", &operation.StepFailure{Detail: "Review the failed workspace updates before advancing the controller release."}
	case "completed":
		return "All retained maintenance targets are settled.", nil
	default:
		return "", &operation.StepFailure{Detail: "The maintenance campaign is no longer active."}
	}
}

// reconcileMaintenanceCampaign derives completion from durable target results.
// An unsettled target keeps the campaign active after a worker restart.
func (s *Store) reconcileMaintenanceCampaign(ctx context.Context, installation, campaign string, now time.Time) (string, error) {
	var state string
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT state FROM maintenance_campaigns WHERE operation_id=$1 AND installation_id=$2`,
			campaign, installation).Scan(&state); err != nil || state != "active" {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE maintenance_targets SET
			state=CASE WHEN (SELECT o.status FROM operations o WHERE o.id=maintenance_targets.operation_id)='succeeded'
				THEN 'completed' ELSE 'failed' END,
			updated_at=$2
			WHERE campaign_id=$1 AND state NOT IN ('completed','failed')
			AND EXISTS (SELECT 1 FROM operations o WHERE o.id=maintenance_targets.operation_id
			AND o.status IN ('succeeded','failed','cancelled'))`, campaign, now.UTC().Format(maintenanceTime))
		if err != nil {
			return err
		}
		var pending, failed bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM maintenance_targets WHERE campaign_id=$1 AND state NOT IN ('completed','failed')),
			EXISTS(SELECT 1 FROM maintenance_targets WHERE campaign_id=$1 AND state='failed')`, campaign).Scan(&pending, &failed); err != nil || pending {
			return err
		}
		state = "completed"
		if failed {
			state = "blocked"
		}
		result, err := tx.ExecContext(ctx, `UPDATE maintenance_campaigns SET state=$2 WHERE operation_id=$1 AND state='active'`, campaign, state)
		return capacityWrite(result, err)
	})
	return state, err
}

// PrepareMaintenanceTargets journals only due targets with no unsettled
// lifecycle work. It shares the admission lock with lifecycle and retention
// so the target owns the workspace before any worker can act on it.
func (s *Store) PrepareMaintenanceTargets(ctx context.Context, journal *operation.Journal, installation, campaign string, now time.Time) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state='active'", installation)
		if err := capacityWrite(result, err); err != nil {
			return err
		}
		stamp := now.UTC().Format(maintenanceTime)
		result, err = tx.ExecContext(ctx, `UPDATE maintenance_campaigns SET state='active'
			WHERE operation_id=$1 AND installation_id=$2 AND deadline<=$3 AND state IN ('scheduled','active')`, campaign, installation, stamp)
		if err := capacityWrite(result, err); err != nil {
			if errors.Is(err, ErrCapacityConflict) {
				return nil
			}
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE maintenance_targets SET state='completed',updated_at=$2
			WHERE campaign_id=$1 AND operation_id IS NULL AND state IN ('pending','waiting_safe')
			AND EXISTS (SELECT 1 FROM workspaces w WHERE w.id=maintenance_targets.workspace_id
			AND w.observed_state='deleted')`, campaign, stamp)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE maintenance_targets SET state='waiting_safe',updated_at=$2
			WHERE campaign_id=$1 AND state='pending'`, campaign, stamp)
		if err != nil {
			return err
		}
		targets, err := readyMaintenanceTargets(ctx, tx, campaign, installation)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if err := prepareMaintenanceTarget(ctx, tx, journal, campaign, target, stamp); err != nil {
				return err
			}
		}
		return nil
	})
}

type maintenanceTarget struct {
	id, desired, observed string
	generation            int64
}

func readyMaintenanceTargets(ctx context.Context, tx *sql.Tx, campaign, installation string) ([]maintenanceTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.desired_state,w.observed_state,w.generation
		FROM maintenance_targets t JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation
		JOIN users u ON u.id=w.owner_user_id
		WHERE t.campaign_id=$1 AND w.installation_id=$2 AND t.state='waiting_safe' AND t.operation_id IS NULL
		AND w.provider_instance_id IS NOT NULL AND w.deletion_commit_at IS NULL AND u.membership_state='active'
		AND w.desired_state=w.observed_state AND w.desired_state IN ('running','stopped')
		AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		AND o.status IN ('pending','running','waiting')) ORDER BY w.id`, campaign, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []maintenanceTarget
	for rows.Next() {
		var target maintenanceTarget
		if err := rows.Scan(&target.id, &target.desired, &target.observed, &target.generation); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func prepareMaintenanceTarget(ctx context.Context, tx *sql.Tx, journal *operation.Journal, campaign string, target maintenanceTarget, stamp string) error {
	steps := []operation.Step{
		{Key: "maintenance_stage", Label: "Stage the signed guest update"},
		{Key: "maintenance_activate", Label: "Activate the guest update safely"},
		{Key: "maintenance_verify", Label: "Verify the updated guest"},
	}
	if target.desired == "stopped" {
		steps = append([]operation.Step{{Key: "maintenance_boot", Label: "Start workspace management services"}}, steps...)
		steps = append(steps, operation.Step{Key: "maintenance_restore", Label: "Return workspace to stopped"})
	}
	key := fmt.Sprintf("maintenance:%x", sha256.Sum256(fmt.Appendf(nil, "%q/%q/%d", campaign, target.id, target.generation)))
	op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: "controller", Key: key, Type: "update",
		OriginatingAction: maintenanceTargetAction, WorkspaceID: &target.id, Generation: target.generation, Steps: steps})
	if err != nil {
		return err
	}
	if op.WorkspaceID == nil || *op.WorkspaceID != target.id || op.Generation != target.generation || op.OriginatingAction != maintenanceTargetAction {
		return operation.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE maintenance_targets
		SET operation_id=$1,prior_desired_state=$2,prior_observed_state=$3,updated_at=$4
		WHERE campaign_id=$5 AND workspace_id=$6 AND generation=$7 AND operation_id IS NULL`,
		op.ID, target.desired, target.observed, stamp, campaign, target.id, target.generation)
	return capacityWrite(result, err)
}

// ListMaintenance returns current and historical campaigns newest first.
func ListMaintenance(ctx context.Context, tx *sql.Tx, installation string) ([]MaintenanceCampaign, error) {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id,release_id,deadline,state FROM maintenance_campaigns
		WHERE installation_id=$1 ORDER BY created_at DESC,operation_id DESC`, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MaintenanceCampaign{}
	for rows.Next() {
		var item MaintenanceCampaign
		var deadline string
		if err := rows.Scan(&item.OperationID, &item.ReleaseID, &deadline, &item.State); err != nil {
			return nil, err
		}
		item.ID = item.OperationID
		item.Deadline, err = time.Parse(maintenanceTime, deadline)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
