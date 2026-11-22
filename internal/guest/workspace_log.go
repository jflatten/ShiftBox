package guest

import (
	"context"
	"crypto/x509"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

// AuthorizeWorkspaceLog binds an upload to a current guest and a dispatched
// configure step. Recently finished dispatches can still flush the final output.
func (e *Enrollment) AuthorizeWorkspaceLog(ctx context.Context, leaf *x509.Certificate, now time.Time, operationID string, version int64) (string, error) {
	var workspaceID string
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now, false)
		if err != nil {
			return err
		}
		if contact.DesiredState != "running" {
			return ErrGuestUnauthorized
		}
		var allowed bool
		cutoff := now.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000000000Z")
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM operations o JOIN guest_dispatches g ON g.operation_id=o.id
			JOIN operation_steps s ON s.operation_id=o.id AND s.step_key='configure'
			WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4
			  AND o.type IN ('provision','start','rebuild')
			  AND (o.status IN ('running','waiting')
			    OR (o.status IN ('succeeded','failed') AND o.finished_at >= $5)))`,
			operationID, contact.WorkspaceID, contact.Generation, version, cutoff).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrGuestUnauthorized
		}
		workspaceID = contact.WorkspaceID
		return nil
	})
	return workspaceID, err
}

type workspaceLogOutput struct {
	operationID string
	version     int64
	text        string
}

type workspaceLogReport struct {
	Format          string `json:"format"`
	OperationID     string `json:"operationId"`
	DispatchVersion int64  `json:"dispatchVersion"`
	BatchID         string `json:"batchId"`
	Text            string `json:"text"`
}

func queueWorkspaceLog(queue chan<- workspaceLogOutput, dispatch guestDispatch, text string) {
	select {
	case queue <- workspaceLogOutput{operationID: dispatch.ID, version: dispatch.Version, text: text}:
	default:
		// A stalled upload must never back up the subprocess stderr pipe.
	}
}

func runWorkspaceLogUploads(ctx context.Context, client *http.Client, endpoint string, queue <-chan workspaceLogOutput) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var current workspaceLogOutput
	var batch strings.Builder
	flush := func() {
		if batch.Len() == 0 {
			return
		}
		report := workspaceLogReport{Format: workspacelog.Format, OperationID: current.operationID, DispatchVersion: current.version,
			BatchID: uuid.NewString(), Text: batch.String()}
		batch.Reset()
		requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		sendGuestDiagnostic(requestCtx, client, endpoint, report, 200*time.Millisecond)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case output := <-queue:
			if output.operationID != current.operationID || output.version != current.version ||
				batch.Len()+len(output.text) > workspacelog.BatchLimit {
				flush()
			}
			current = output
			batch.WriteString(output.text)
		case <-tick.C:
			flush()
		}
	}
}
