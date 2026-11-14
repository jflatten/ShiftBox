package guest

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/template"
)

// ErrGuestReportConflict identifies an invalid, stale, or regressing report.
var ErrGuestReportConflict = errors.New("guest report conflicts with dispatched operation")

// PollReport carries the last acknowledged dispatch and typed operation evidence.
// The workspace and generation authority comes from the verified TLS leaf.
type PollReport struct {
	LastOperationID string
	Version         int64
	Operation       *OperationReport
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
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		var err error
		contact, err = e.authenticate(ctx, tx, leaf, now)
		if err != nil {
			return err
		}
		if err := acceptConfigurationReport(ctx, tx, contact, report); err != nil {
			return err
		}
		dispatch, err = nextConfiguration(ctx, tx, contact)
		return err
	})
	if err != nil {
		return Contact{}, nil, err
	}
	return contact, dispatch, nil
}

func nextConfiguration(ctx context.Context, tx *sql.Tx, contact Contact) (*PollOperation, error) {
	if contact.DesiredState != "running" {
		return nil, nil
	}
	dispatch := PollOperation{Command: "ensure-development-container"}
	// The provider worker records configure as active after enrollment. It may
	// release and reacquire its lease while the guest works.
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.type,o.version,w.repository_url,COALESCE(w.repository_ref,''),w.devcontainer_path
		FROM operations o JOIN workspaces w ON w.id=o.workspace_id
		JOIN operation_steps s ON s.operation_id=o.id AND s.step_key='configure'
		LEFT JOIN guest_configurations g ON g.operation_id=o.id
		WHERE w.id=$1 AND w.generation=$2 AND o.generation=$2 AND w.observed_state='running'
		  AND o.type='provision' AND o.originating_action='workspace.launch' AND o.requested_by=w.owner_user_id
		  AND o.current_step='configure' AND s.status='active' AND o.status IN ('running','waiting')
		  AND (g.status IS NULL OR g.status IN ('pending','running')) LIMIT 2`, contact.WorkspaceID, contact.Generation)
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
	if count != 1 || !template.ValidSource(dispatch.Arguments.RepositoryURL, dispatch.Arguments.Devcontainer) {
		return nil, ErrGuestReportConflict
	}
	dispatch.Arguments.RetainedMounts = []string{}
	// Freeze the dispatched version across worker journal and lease updates.
	if _, err := tx.ExecContext(ctx, `INSERT INTO guest_configurations(operation_id,dispatch_version,status)
		VALUES($1,$2,'pending') ON CONFLICT(operation_id) DO NOTHING`, dispatch.ID, dispatch.Version); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT dispatch_version FROM guest_configurations WHERE operation_id=$1`, dispatch.ID).
		Scan(&dispatch.Version)
	return &dispatch, err
}

func acceptConfigurationReport(ctx context.Context, tx *sql.Tx, contact Contact, input PollReport) error {
	report := input.Operation
	if report == nil {
		if input.LastOperationID != "" || input.Version != 0 {
			return ErrGuestReportConflict
		}
		return nil
	}
	if report.ID == "" || report.ID != input.LastOperationID || input.Version < 1 ||
		report.Generation != contact.Generation || !validConfigurationReport(*report) {
		return ErrGuestReportConflict
	}
	var status, container, problem, workflow, step string
	err := tx.QueryRowContext(ctx, `SELECT g.status,g.container_id,g.problem,o.status,COALESCE(o.current_step,'')
		FROM guest_configurations g JOIN operations o ON o.id=g.operation_id
		WHERE o.id=$1 AND o.workspace_id=$2 AND o.generation=$3 AND g.dispatch_version=$4`,
		report.ID, contact.WorkspaceID, contact.Generation, input.Version).Scan(&status, &container, &problem, &workflow, &step)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGuestReportConflict
	}
	if err != nil {
		return err
	}
	if status == report.Status && container == report.Evidence.ContainerID && problem == report.Problem {
		return nil
	}
	if status == "failed" || status == "succeeded" || workflow != "running" && workflow != "waiting" ||
		step != "configure" || contact.DesiredState != "running" ||
		status == "running" && container != report.Evidence.ContainerID {
		return ErrGuestReportConflict
	}
	if report.Status != "running" && report.Evidence.WaitForReached && status != "running" {
		return ErrGuestReportConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE guest_configurations SET status=$1,container_id=$2,problem=$3 WHERE operation_id=$4`,
		report.Status, report.Evidence.ContainerID, report.Problem, report.ID)
	return err
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
			(report.Problem == problemRequired || report.Problem == problemInterrupted || report.Problem == problemRepository)
	default:
		return false
	}
}
