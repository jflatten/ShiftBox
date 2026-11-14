package guest

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// UpdateDownloadPath is served only by the authenticated guest listener.
const UpdateDownloadPath = "/internal/v1/guest/update"

// UpdateRelease checks the current registration and resolves the exact release
// assigned to the authenticated workspace's active maintenance stage.
func (e *Enrollment) UpdateRelease(ctx context.Context, leaf *x509.Certificate, now time.Time) (string, error) {
	var release string
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now, false)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT a.guest_release FROM maintenance_targets t
			JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id AND c.state='active'
			JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation AND w.installation_id=c.installation_id
			JOIN installations i ON i.id=c.installation_id
			JOIN platform_artifacts a ON a.release_id=c.release_id AND a.guest_release=i.active_release AND a.state='available'
			JOIN operations o ON o.id=t.operation_id AND o.workspace_id=w.id AND o.generation=w.generation
			JOIN operation_steps s ON s.operation_id=o.id AND s.step_key=o.current_step AND s.status='active'
			WHERE w.id=$1 AND w.generation=$2 AND w.deletion_commit_at IS NULL
			AND t.state IN ('waiting_safe','updating') AND o.originating_action='maintenance.update_guest'
			AND o.current_step='maintenance_stage' AND o.status IN ('running','waiting')`, contact.WorkspaceID, contact.Generation).Scan(&release)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrGuestUnauthorized
	}
	return release, err
}
