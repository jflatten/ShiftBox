package workspace

import (
	"context"
	"database/sql"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// The worker journals step entry before any provider call. Only a cancelled
// launch that never entered provision proves that no VM or disk was requested.
const unprovisionedWorkspace = `w.provider_instance_id IS NULL
	AND NOT EXISTS (SELECT 1 FROM provider_resources r WHERE r.workspace_id=w.id)
	AND EXISTS (SELECT 1 FROM operations o JOIN operation_steps s ON s.operation_id=o.id
		WHERE o.workspace_id=w.id AND o.generation=w.generation AND o.originating_action='workspace.launch'
		  AND o.status='cancelled' AND s.step_key='provision' AND s.status='skipped' AND s.started_at IS NULL)`

func recordUnprovisionedPower(ctx context.Context, store *Store, op operation.Operation, state string, now time.Time) (bool, error) {
	var completed bool
	err := database.Transaction(ctx, store.db, func(tx *sql.Tx) error {
		stamp := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE workspaces AS w SET observed_state=$1,
			updated_at=CASE WHEN observed_state<>$1 THEN $2 ELSE updated_at END,
			version=CASE WHEN observed_state<>$1 THEN version+1 ELSE version END
			WHERE w.id=$3 AND w.generation=$4 AND w.desired_state=$1 AND (`+unprovisionedWorkspace+`)
			  AND EXISTS (SELECT 1 FROM installations i WHERE i.id=w.installation_id AND i.state='active')
			  AND EXISTS (SELECT 1 FROM operations o WHERE o.id=$5 AND o.workspace_id=w.id
			    AND o.generation=w.generation AND o.originating_action=$6 AND o.status='running' AND o.current_step=$7)`,
			state, stamp, *op.WorkspaceID, op.Generation, op.ID, op.OriginatingAction, op.CurrentStep)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE reservations SET cpu_memory_state='released',disk_state='released',
			cpu_memory_released_at=COALESCE(cpu_memory_released_at,$2),disk_released_at=COALESCE(disk_released_at,$2)
			WHERE workspace_id=$1`, *op.WorkspaceID, stamp)
		completed = err == nil
		return err
	})
	return completed, err
}
