package workspace

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

var errIDEProbePending = errors.New("waiting for a working browser IDE proxy")

// IDEProbe verifies a workbench on the authenticated workspace tunnel.
type IDEProbe func(context.Context, string, int64) error

func verifyIDE(ctx context.Context, db *sql.DB, op operation.Operation, probe IDEProbe) (string, error) {
	if op.WorkspaceID == nil || op.Generation < 1 || probe == nil {
		return "", provider.ErrInvalidState
	}
	var eligible bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM workspaces w JOIN installations i ON i.id=w.installation_id
		JOIN users u ON u.id=w.owner_user_id JOIN guest_registrations r ON r.workspace_id=w.id AND r.generation=w.generation
		JOIN operations o ON o.workspace_id=w.id AND o.generation=w.generation
		JOIN guest_dispatches d ON d.operation_id=o.id JOIN operation_steps s ON s.operation_id=o.id AND s.step_key='configure'
		WHERE w.id=$1 AND w.generation=$2 AND o.id=$3 AND o.status='running' AND o.current_step='verify_ide'
		  AND w.desired_state='running' AND w.observed_state='running' AND w.provider_instance_id IS NOT NULL
		  AND i.state='active' AND u.membership_state='active' AND r.state='active'
		  AND s.status='succeeded' AND d.container_id<>''
	)`, *op.WorkspaceID, op.Generation, op.ID).Scan(&eligible)
	if err != nil {
		return "", err
	}
	if !eligible {
		return "", errIDEProbePending
	}
	if err := probe(ctx, *op.WorkspaceID, op.Generation); err != nil {
		return "", errors.Join(errIDEProbePending, err)
	}
	return "The authenticated guest tunnel returned the browser IDE workbench.", nil
}
