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

const retentionDeleteAction = "workspace.retention_delete"

// CommitRetentionDeletion fences the final policy check with the workspace's
// deletion intent, audit record and durable operation. Provider effects belong
// to the subsequent worker, after this transaction commits.
func (s *Store) CommitRetentionDeletion(ctx context.Context, journal *operation.Journal, candidate RetentionCandidate,
	now time.Time, policy RetentionEligibility,
) (operation.Operation, error) {
	if journal == nil || !candidate.verified || !policy.ready(now) {
		return operation.Operation{}, sql.ErrNoRows
	}
	var committed operation.Operation
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		ref := candidate.Ref
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1", ref.InstallationID); err != nil {
			return err
		}
		current, err := recheckRetention(ctx, tx, candidate, now, policy)
		if err != nil {
			return err
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET desired_state='deleted',deletion_commit_at=$1,
			retention_deadline=$2,updated_at=$1,version=version+1
			WHERE id=$3 AND generation=$4 AND version=$5 AND deletion_commit_at IS NULL
			AND COALESCE(last_activity_at,created_at)=$6 AND retention_seconds=$7`,
			stamp, current.Deadline.UTC().Format(time.RFC3339Nano), ref.WorkspaceID, ref.Generation, current.version,
			current.baseline, current.seconds)
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
		committed, err = journal.CreateInTx(ctx, tx, operation.Request{
			Actor: "controller", Key: "retention-delete:" + ref.WorkspaceID + ":" + strconv.FormatInt(ref.Generation, 10),
			Type: "delete", OriginatingAction: retentionDeleteAction, WorkspaceID: &ref.WorkspaceID, Generation: ref.Generation,
			Steps: []operation.Step{
				{Key: "shutdown", Label: "Request graceful shutdown"},
				{Key: "stop", Label: "Verify workspace stopped"},
				{Key: "delete", Label: "Delete workspace and retained files"},
			},
		})
		if err != nil {
			return err
		}
		if committed.WorkspaceID == nil || *committed.WorkspaceID != ref.WorkspaceID || committed.Generation != ref.Generation ||
			committed.Type != "delete" || committed.OriginatingAction != retentionDeleteAction || committed.Status != "pending" {
			return operation.ErrConflict
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "controller", ActorID: ref.InstallationID,
			Action: retentionDeleteAction, ResourceType: "workspace", ResourceID: ref.WorkspaceID, Outcome: "allowed",
			ReasonCode: "retention_deadline_elapsed"})
		return err
	})
	if err != nil {
		return operation.Operation{}, err
	}
	return committed, nil
}
