package workspace

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
)

var (
	// ErrMaintenanceInvalid indicates an invalid campaign intent.
	ErrMaintenanceInvalid = errors.New("invalid maintenance campaign")
	// ErrMaintenanceUnavailable indicates the release cannot be scheduled or work is already scheduled.
	ErrMaintenanceUnavailable = errors.New("maintenance campaign unavailable")
)

const maintenanceTime = "2006-01-02T15:04:05.000000000Z"

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
	var available bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM installations i JOIN platform_artifacts a ON a.release_id=$2 AND a.guest_release=i.active_release
		WHERE i.id=$1 AND a.state='available' AND NOT EXISTS (
			SELECT 1 FROM maintenance_campaigns c WHERE c.installation_id=i.id AND c.state IN ('scheduled','active','blocked')
		))`, installation, releaseID).Scan(&available)
	if err != nil {
		return operation.Operation{}, err
	}
	if !available {
		return operation.Operation{}, ErrMaintenanceUnavailable
	}
	op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: actor, Key: key, Type: "update",
		OriginatingAction: "maintenance.manage", Generation: 1,
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
		WHERE installation_id=$2 AND observed_state<>'deleted'`, op.ID, installation, created)
	return op, err
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
