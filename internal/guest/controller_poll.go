package guest

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/template"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// ErrGuestReportConflict identifies an invalid, stale, or regressing report.
var ErrGuestReportConflict = errors.New("guest report conflicts with dispatched operation")

// PollReport carries the last acknowledged dispatch and typed operation evidence.
// The workspace and generation authority comes from the verified TLS leaf.
type PollReport struct {
	GuestRelease    string
	ProtocolVersion string
	LastOperationID string
	Version         int64
	Operation       *OperationReport
	Activity        *workspace.CPUActivity
	IDEConnected    func(workspaceID string) bool
}

// PollOperation is one controller-approved action and its workspace snapshot.
type PollOperation struct {
	ID        string        `json:"id"`
	Version   int64         `json:"version"`
	Type      string        `json:"type"`
	Command   string        `json:"command"`
	Arguments pollArguments `json:"arguments"`
}

type pollArguments struct {
	RepositoryURL  string   `json:"repositoryUrl"`
	RepositoryRef  string   `json:"repositoryRef"`
	Devcontainer   string   `json:"devcontainerPath"`
	RetainedMounts []string `json:"retainedMounts"`
}

// Exchange rechecks the current leaf and reconciles one report and dispatch
// in the same transaction. The payload never selects a workspace.
func (e *Enrollment) Exchange(ctx context.Context, leaf *x509.Certificate, now time.Time, report PollReport) (Contact, *PollOperation, error) {
	var contact Contact
	var dispatch *PollOperation
	// Proxy admission takes the registry lock before checking the database.
	// Read presence in the same order, then authenticate the workspace in SQL.
	ideConnected := false
	if report.IDEConnected != nil && leaf != nil && len(leaf.URIs) == 1 {
		if parts := strings.Split(leaf.URIs[0].Path, "/"); len(parts) == 4 {
			ideConnected = report.IDEConnected(parts[2])
		}
	}
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		var err error
		contact, err = e.authenticate(ctx, tx, leaf, now, true)
		if err != nil {
			return err
		}
		if err := acceptGuestReport(ctx, tx, contact, report); err != nil {
			return err
		}
		// Keep the last accepted release even when a guest stops polling.
		result, err := tx.ExecContext(ctx, `UPDATE workspaces SET guest_release=$1,protocol_version=$2
			WHERE id=$3 AND generation=$4`, report.GuestRelease, report.ProtocolVersion, contact.WorkspaceID, contact.Generation)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			return ErrGuestUnauthorized
		}
		if err := workspace.RecordActivity(ctx, tx, contact.WorkspaceID, contact.Generation, now,
			workspace.ActivityPolicy{IDE: true, CPU: true, CPUThreshold: 5, Freshness: 90 * time.Second}, report.Activity, ideConnected); err != nil {
			return err
		}
		dispatch, err = nextGuestOperation(ctx, tx, contact)
		return err
	})
	if err != nil {
		return Contact{}, nil, err
	}
	if contact.DesiredState == "deleted" {
		// Existing agents already understand the graceful-stop command.
		contact.DesiredState = "stopped"
		if dispatch != nil {
			dispatch.Type = "stop"
		}
	}
	return contact, dispatch, nil
}

func nextGuestOperation(ctx context.Context, tx *sql.Tx, contact Contact) (*PollOperation, error) {
	dispatch := PollOperation{}
	var step string
	switch contact.DesiredState {
	case "running":
		dispatch.Command, step = "ensure-development-container", "configure"
	case "stopped", "deleted":
		dispatch.Command, step = "shutdown-for-stop", "shutdown"
	default:
		return nil, nil
	}
	// The worker records the active step before the poll can dispatch it.
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.type,o.version,w.repository_url,COALESCE(w.repository_ref,''),w.devcontainer_path
		FROM operations o JOIN workspaces w ON w.id=o.workspace_id
		JOIN operation_steps s ON s.operation_id=o.id AND s.step_key=$3
		LEFT JOIN guest_dispatches g ON g.operation_id=o.id
		WHERE w.id=$1 AND w.generation=$2 AND o.generation=$2
		  AND (w.observed_state='running' OR ($4='deleted' AND w.observed_state IN ('stopped','unreachable')))
		  AND w.desired_state=$4
		  AND (($3='configure' AND o.type='provision' AND o.originating_action='workspace.launch' AND o.requested_by=w.owner_user_id)
		    OR ($3='shutdown' AND $4='deleted' AND o.type='delete' AND o.originating_action='workspace.retention_delete'
		      AND w.deletion_commit_at IS NOT NULL)
		    OR (($3='shutdown' AND $4='stopped' AND o.type='stop' AND o.originating_action='workspace.stop'
		      OR $3='configure' AND o.type='start' AND o.originating_action='workspace.start')
		      AND EXISTS(SELECT 1 FROM users actor WHERE actor.id=o.requested_by AND actor.membership_state='active'
		        AND (actor.id=w.owner_user_id OR actor.role='administrator'))))
		  AND o.status IN ('running','waiting')
		  AND ((o.current_step=$3 AND s.status='active')
		    OR ($3='configure' AND o.current_step IN ('verify_ide','finish_setup')
		        AND s.status='succeeded' AND g.status='running'))
		  AND (g.status IS NULL OR g.status IN ('pending','running')) LIMIT 2`, contact.WorkspaceID, contact.Generation, step, contact.DesiredState)
	if err != nil {
		return nil, err
	}
	var count int
	for rows.Next() {
		count++
		if err = rows.Scan(&dispatch.ID, &dispatch.Type, &dispatch.Version,
			&dispatch.Arguments.RepositoryURL, &dispatch.Arguments.RepositoryRef, &dispatch.Arguments.Devcontainer); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || count == 0 {
		return nil, err
	}
	if count != 1 || step == "configure" && !template.ValidSource(dispatch.Arguments.RepositoryURL, dispatch.Arguments.Devcontainer) {
		return nil, ErrGuestReportConflict
	}
	dispatch.Arguments.RetainedMounts = []string{}
	if step == "shutdown" {
		dispatch.Arguments = pollArguments{RetainedMounts: []string{}}
	}
	// Freeze the dispatched version across worker journal and lease updates.
	if _, err := tx.ExecContext(ctx, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status)
		VALUES($1,$2,'pending') ON CONFLICT(operation_id) DO NOTHING`, dispatch.ID, dispatch.Version); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT dispatch_version FROM guest_dispatches WHERE operation_id=$1`, dispatch.ID).
		Scan(&dispatch.Version)
	return &dispatch, err
}

func acceptGuestReport(ctx context.Context, tx *sql.Tx, contact Contact, input PollReport) error {
	report := input.Operation
	if report == nil {
		if input.LastOperationID != "" || input.Version != 0 {
			return ErrGuestReportConflict
		}
		return nil
	}
	shutdown := report.Step == "workload.shutdown"
	if len(report.ID) != 36 || report.Attempt != 1 || report.ID != input.LastOperationID || input.Version < 1 ||
		report.Generation != contact.Generation || shutdown && !validShutdownReport(*report) || !shutdown && !validConfigurationReport(*report) {
		return ErrGuestReportConflict
	}
	var status, container, problem, workflow, step, kind string
	err := tx.QueryRowContext(ctx, `SELECT g.status,g.container_id,g.problem,o.status,COALESCE(o.current_step,''),o.type
		FROM guest_dispatches g JOIN operations o ON o.id=g.operation_id
		WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4`,
		report.ID, contact.WorkspaceID, contact.Generation, input.Version).Scan(&status, &container, &problem, &workflow, &step, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGuestReportConflict
	}
	if err != nil {
		return err
	}
	if (shutdown && kind != "stop" && kind != "delete") || (!shutdown && kind != "provision" && kind != "start") {
		return ErrGuestReportConflict
	}
	if status == report.Status && container == report.Evidence.ContainerID && problem == report.Problem {
		return nil
	}
	expectedStep, expectedDesired := "configure", "running"
	if shutdown {
		expectedStep, expectedDesired = "shutdown", "stopped"
		if kind == "delete" {
			expectedDesired = "deleted"
		}
	}
	inStage := step == expectedStep ||
		!shutdown && status == "running" && (step == "verify_ide" || step == "finish_setup")
	if status == "failed" || status == "succeeded" || workflow != "running" && workflow != "waiting" ||
		!inStage || contact.DesiredState != expectedDesired ||
		status == "running" && container != report.Evidence.ContainerID {
		return ErrGuestReportConflict
	}
	if report.Status != "running" && report.Evidence.WaitForReached && status != "running" {
		return ErrGuestReportConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE guest_dispatches SET status=$1,container_id=$2,problem=$3 WHERE operation_id=$4`,
		report.Status, report.Evidence.ContainerID, report.Problem, report.ID)
	return err
}

func validShutdownReport(report OperationReport) bool {
	return report.Evidence == (localEvidence{}) &&
		(report.Status == "succeeded" && report.Problem == "" ||
			report.Status == "failed" && (report.Problem == problemShutdownFailed || report.Problem == problemShutdownTimeout))
}

func validConfigurationReport(report OperationReport) bool {
	id := report.Evidence.ContainerID
	reached := report.Evidence.WaitForReached
	if len(report.ID) != 36 || report.Attempt != 1 || reached != (id != "") ||
		reached && !dockerContainerID.MatchString(id) {
		return false
	}
	switch report.Status {
	case "running":
		return reached && report.Step == "devcontainer.wait_for" && report.Problem == ""
	case "succeeded":
		return reached && report.Step == "devcontainer.lifecycle" && report.Problem == ""
	case "failed":
		if reached {
			return report.Step == "devcontainer.lifecycle" && (report.Problem == problemRemaining || report.Problem == problemInterrupted)
		}
		return report.Step == "devcontainer.wait_for" &&
			(report.Problem == problemRequired || report.Problem == problemInterrupted || report.Problem == problemRepository || report.Problem == problemIDE)
	default:
		return false
	}
}
