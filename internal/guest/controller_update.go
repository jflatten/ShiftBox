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
		_, _, release, err = activeGuestUpdate(ctx, tx, contact, "maintenance_stage")
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrGuestUnauthorized
	}
	return release, err
}

func activeGuestUpdate(ctx context.Context, tx *sql.Tx, contact Contact, step string) (id string, version int64, release string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.version,a.guest_release FROM maintenance_targets t
			JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id AND c.state='active'
			JOIN workspaces w ON w.id=t.workspace_id AND w.generation=t.generation AND w.installation_id=c.installation_id
			JOIN installations i ON i.id=c.installation_id
			JOIN platform_artifacts a ON a.release_id=c.release_id AND a.guest_release=i.active_release AND a.state='available'
			JOIN operations o ON o.id=t.operation_id AND o.workspace_id=w.id AND o.generation=w.generation
			JOIN operation_steps s ON s.operation_id=o.id AND s.step_key=o.current_step AND s.status='active'
			WHERE w.id=$1 AND w.generation=$2 AND w.deletion_commit_at IS NULL
			AND t.state IN ('waiting_safe','updating') AND o.originating_action='maintenance.update_guest'
			AND o.type='update' AND o.current_step=$3 AND o.status IN ('running','waiting')`,
		contact.WorkspaceID, contact.Generation, step).Scan(&id, &version, &release)
	return id, version, release, err
}

func nextGuestUpdateStage(ctx context.Context, tx *sql.Tx, contact Contact) (*PollOperation, error) {
	id, version, release, err := activeGuestUpdate(ctx, tx, contact, "maintenance_stage")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status)
		VALUES($1,$2,'pending') ON CONFLICT(operation_id) DO NOTHING`, id, version); err != nil {
		return nil, err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT dispatch_version,status FROM guest_dispatches WHERE operation_id=$1`, id).
		Scan(&version, &status); err != nil || status != "pending" {
		return nil, err
	}
	return &PollOperation{ID: id, Version: version, Type: "update", Command: stageGuestUpdateCommand,
		Arguments: pollArguments{ProductRelease: release, RetainedMounts: []string{}}}, nil
}

func acceptGuestUpdateStage(ctx context.Context, tx *sql.Tx, contact Contact, input PollReport) error {
	r := input.Operation
	digest := r.Evidence.UpdateDigest
	if !validGuestUpdateReceipt(r, contact, input) ||
		!(r.Status == "succeeded" && validStagedUpdateDigest(digest) && r.Problem == "" ||
			r.Status == "failed" && digest == "" && r.Problem == problemGuestUpdateStage) {
		return ErrGuestReportConflict
	}
	var status, savedDigest, problem string
	err := tx.QueryRowContext(ctx, `SELECT g.status,g.update_digest,g.problem FROM guest_dispatches g
		JOIN operations o ON o.id=g.operation_id
		WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4
		AND o.type='update' AND o.originating_action='maintenance.update_guest'`, r.ID, contact.WorkspaceID, contact.Generation,
		input.Version).Scan(&status, &savedDigest, &problem)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGuestReportConflict
	}
	if err != nil || status == r.Status && savedDigest == digest && problem == r.Problem {
		return err
	}
	id, _, _, err := activeGuestUpdate(ctx, tx, contact, "maintenance_stage")
	if errors.Is(err, sql.ErrNoRows) || err == nil && (id != r.ID || status != "pending") {
		return ErrGuestReportConflict
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE guest_dispatches SET status=$1,update_digest=$2,problem=$3
		WHERE operation_id=$4 AND status='pending'`, r.Status, digest, r.Problem, r.ID)
	return err
}

func nextGuestUpdateActivation(ctx context.Context, tx *sql.Tx, contact Contact) (*PollOperation, error) {
	id, _, release, err := activeGuestUpdate(ctx, tx, contact, "maintenance_activate")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var version int64
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT dispatch_version,update_digest FROM guest_dispatches
		WHERE operation_id=$1 AND status='succeeded' AND activation_status=''`, id).Scan(&version, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validStagedUpdateDigest(digest) {
		return nil, ErrGuestReportConflict
	}
	return &PollOperation{ID: id, Version: version, Type: "update", Command: activateGuestUpdateCommand,
		Arguments: pollArguments{ProductRelease: release, RetainedMounts: []string{}}}, nil
}

func acceptGuestUpdateActivation(ctx context.Context, tx *sql.Tx, contact Contact, input PollReport) error {
	r := input.Operation
	if !validGuestUpdateReceipt(r, contact, input) || !validStagedUpdateDigest(r.Evidence.UpdateDigest) ||
		!(r.Status == "succeeded" && r.Problem == "" || r.Status == "failed" && r.Problem == problemGuestUpdateRollback) {
		return ErrGuestReportConflict
	}
	var digest, activation string
	err := tx.QueryRowContext(ctx, `SELECT g.update_digest,g.activation_status FROM guest_dispatches g
		JOIN operations o ON o.id=g.operation_id
		WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4
		AND o.type='update' AND o.originating_action='maintenance.update_guest' AND g.status='succeeded'`,
		r.ID, contact.WorkspaceID, contact.Generation, input.Version).Scan(&digest, &activation)
	if errors.Is(err, sql.ErrNoRows) || err == nil && digest != r.Evidence.UpdateDigest {
		return ErrGuestReportConflict
	}
	if err != nil || activation == r.Status {
		return err
	}
	id, _, release, err := activeGuestUpdate(ctx, tx, contact, "maintenance_activate")
	if errors.Is(err, sql.ErrNoRows) || err == nil && (id != r.ID || activation != "" ||
		r.Status == "succeeded" && input.GuestRelease != release) {
		return ErrGuestReportConflict
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE guest_dispatches SET activation_status=$1
		WHERE operation_id=$2 AND activation_status=''`, r.Status, r.ID)
	return err
}

func validGuestUpdateReceipt(r *OperationReport, contact Contact, input PollReport) bool {
	return len(r.ID) == 36 && r.Attempt == 1 && r.ID == input.LastOperationID && input.Version >= 1 &&
		r.Generation == contact.Generation && r.Evidence.ContainerID == "" && !r.Evidence.WaitForReached
}
