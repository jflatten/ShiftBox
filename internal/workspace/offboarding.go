package workspace

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
)

// OffboardingStop identifies the durable provider stop for one workspace.
type OffboardingStop struct {
	WorkspaceID string `json:"workspaceId"`
	OperationID string `json:"operationId"`
}

// QueueOffboardingStops fences running workflows and records a provider stop
// for each unsettled workspace in the membership transaction.
func QueueOffboardingStops(ctx context.Context, tx *sql.Tx, journal *operation.Journal, owner, actor string, membershipVersion int64, now time.Time) ([]string, []OffboardingStop, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,generation,desired_state,observed_state FROM workspaces
		WHERE owner_user_id=$1 ORDER BY id`, owner)
	if err != nil {
		return nil, nil, err
	}
	type workspace struct {
		id                string
		generation        int64
		desired, observed string
	}
	var workspaces []workspace
	for rows.Next() {
		var item workspace
		if err := rows.Scan(&item.id, &item.generation, &item.desired, &item.observed); err != nil {
			rows.Close()
			return nil, nil, err
		}
		workspaces = append(workspaces, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(workspaces))
	stops := make([]OffboardingStop, 0, len(workspaces))
	for _, item := range workspaces {
		ids = append(ids, item.id)
		if item.desired == "deleted" || item.observed == "deleted" || (item.desired == "stopped" && item.observed == "stopped") {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workspaces SET desired_state='stopped',version=version+1,updated_at=$2
			WHERE id=$1 AND desired_state<>'stopped'`, item.id, now.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, nil, err
		}
		// An earlier offboarding stop already owns this work. Cancel other
		// active lifecycle operations so none can issue a later start.
		rows, err := tx.QueryContext(ctx, `SELECT id,originating_action FROM operations WHERE workspace_id=$1 AND generation=$2
			AND status IN ('pending','running','waiting') ORDER BY id`, item.id, item.generation)
		if err != nil {
			return nil, nil, err
		}
		var stopID string
		var superseded []string
		for rows.Next() {
			var id, action string
			if err := rows.Scan(&id, &action); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if action == "user.disable" {
				stopID = id
			} else {
				superseded = append(superseded, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
		for _, id := range superseded {
			if err := journal.CancelInTx(ctx, tx, id, now); err != nil {
				return nil, nil, err
			}
		}
		if stopID == "" {
			op, err := journal.CreateInTx(ctx, tx, operation.Request{
				Actor: actor, Key: "offboard/" + item.id + "/" + strconv.FormatInt(membershipVersion, 10),
				Type: "stop", OriginatingAction: "user.disable", WorkspaceID: &item.id, Generation: item.generation,
				Steps: []operation.Step{{Key: "stop", Label: "Stop workspace"}},
			})
			if err != nil {
				return nil, nil, err
			}
			stopID = op.ID
		}
		stops = append(stops, OffboardingStop{WorkspaceID: item.id, OperationID: stopID})
	}
	return ids, stops, nil
}
