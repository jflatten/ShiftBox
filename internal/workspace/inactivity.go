package workspace

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const inactivityStopAction = "workspace.inactivity_stop"

// InactivityCandidates discovers overdue running workspaces. Discovery grants
// no shutdown authority; commitment rereads current activity and policy.
func (s *Store) InactivityCandidates(ctx context.Context, installation string, now time.Time) ([]string, error) {
	policy, err := ReadKeepAlivePolicy(ctx, s.db)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, inactivityProjection+" AND w.installation_id=$1 ORDER BY w.id", installation, now.UTC().Format(maintenanceTime))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var item inactivityWorkspace
		if err := rows.Scan(&item.id, &item.installation, &item.generation, &item.version, &item.baseline, &item.override); err != nil {
			return nil, err
		}
		due, err := item.due(policy, now)
		if err != nil {
			return nil, err
		}
		if due {
			result = append(result, item.id)
		}
	}
	return result, rows.Err()
}

// CommitInactivityStop records the current policy decision, audit, and durable
// stop atomically. The worker verifies provider identity before stopping the VM.
func (s *Store) CommitInactivityStop(ctx context.Context, journal *operation.Journal, id string, now time.Time) (operation.Operation, error) {
	var op operation.Operation
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		// Serialize with policy edits, start/stop requests and maintenance admission.
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
			return err
		}
		policy, err := ReadKeepAlivePolicy(ctx, tx)
		if err != nil {
			return err
		}
		var item inactivityWorkspace
		err = tx.QueryRowContext(ctx, inactivityProjection+" AND w.id=$1", id, now.UTC().Format(maintenanceTime)).
			Scan(&item.id, &item.installation, &item.generation, &item.version, &item.baseline, &item.override)
		if err != nil {
			return err
		}
		due, err := item.due(policy, now)
		if err != nil {
			return err
		}
		if !due {
			return sql.ErrNoRows
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET desired_state='stopped',updated_at=$1,version=version+1
			WHERE id=$2 AND generation=$3 AND version=$4 AND desired_state='running'
			AND COALESCE(last_activity_at,created_at)=$5 AND deletion_commit_at IS NULL`,
			stamp, id, item.generation, item.version, item.baseline)
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
		// Ordinary provider stop preserves disks and can shut down a VM even
		// when its guest can no longer acknowledge a workload shutdown request.
		op, err = journal.CreateInTx(ctx, tx, operation.Request{
			Actor: "controller", Key: "inactivity-stop:" + id + ":" + strconv.FormatInt(item.version, 10),
			Type: "stop", OriginatingAction: inactivityStopAction, WorkspaceID: &id, Generation: item.generation,
			Steps: []operation.Step{{Key: "stop", Label: "Stop inactive workspace"}},
		})
		if err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "controller", ActorID: item.installation,
			Action: inactivityStopAction, ResourceType: "workspace", ResourceID: id, Outcome: "allowed",
			ReasonCode: "keep_alive_elapsed"})
		return err
	})
	if err != nil {
		return operation.Operation{}, err
	}
	return op, nil
}

type inactivityWorkspace struct {
	id, installation, baseline string
	generation, version        int64
	override                   *int64
}

func (w inactivityWorkspace) due(policy KeepAlivePolicy, now time.Time) (bool, error) {
	minutes := policy.EffectiveMinutes(w.override)
	if minutes == 0 {
		return false, nil
	}
	baseline, err := time.Parse(time.RFC3339Nano, w.baseline)
	if err != nil {
		return false, err
	}
	return now.After(baseline.Add(time.Duration(minutes) * time.Minute)), nil
}

const inactivityProjection = `SELECT w.id,w.installation_id,w.generation,w.version,
	COALESCE(w.last_activity_at,w.created_at),w.keep_alive_minutes
	FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN users u ON u.id=w.owner_user_id
	WHERE i.state='active' AND u.membership_state='active' AND w.desired_state='running'
	AND w.observed_state IN ('running','unreachable') AND w.provider_instance_id IS NOT NULL
	AND w.deletion_commit_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		AND o.status IN ('pending','running','waiting'))
	AND NOT EXISTS (` + dueMaintenanceTarget + ` AND c.deadline<=$2)`
