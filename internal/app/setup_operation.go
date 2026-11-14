package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// AdministratorAdmission is safe workflow metadata; it contains no credentials.
type AdministratorAdmission struct {
	AdministratorID string              `json:"administratorId"`
	Operation       operation.Operation `json:"operation"`
}

// BeginAdministrator atomically binds the single initial-owner intent to its
// visible workflow. A retry with a new HTTP key still resumes that same workflow.
// The protected local endpoint must admit the request before calling this service.
func BeginAdministrator(ctx context.Context, db *sql.DB, journal *operation.Journal, input AdministratorIntent, cipher *cryptostore.Store, requestKey string) (AdministratorAdmission, error) {
	var result AdministratorAdmission
	if db == nil || journal == nil || len(requestKey) < 16 || len(requestKey) > 128 {
		return result, ErrSetupConflict
	}
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		record, err := PrepareAdministrator(ctx, tx, input, cipher)
		if err != nil {
			return err
		}
		result.AdministratorID = record.UserID
		var id sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM bootstrap_administrator WHERE installation_id=$1`, input.InstallationID).Scan(&id); err != nil {
			return err
		}
		if id.Valid {
			result.Operation, err = journal.GetInTx(ctx, tx, id.String)
			return err
		}
		result.Operation, err = journal.CreateInTx(ctx, tx, operation.Request{Actor: "local_setup", Key: requestKey, Type: "setup", OriginatingAction: "setup.mutate", Generation: 1, Steps: []operation.Step{{Key: "administrator", Label: "Record initial administrator"}, {Key: "credentials", Label: "Configure local sign-in"}}})
		if err != nil {
			return err
		}
		if result.Operation.Type != "setup" || len(result.Operation.Steps) != 2 || result.Operation.Steps[0].Key != "administrator" || result.Operation.Steps[1].Key != "credentials" {
			return ErrSetupConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE bootstrap_administrator SET operation_id=$1 WHERE installation_id=$2`, result.Operation.ID, input.InstallationID)
		return err
	})
	if err != nil {
		return AdministratorAdmission{}, err
	}
	return result, nil
}

// AdministratorExecutor adapts durable initial-owner reconciliation to the shared
// worker. Evidence is non-secret and derives from the linked setup record.
type AdministratorExecutor struct {
	DB             *sql.DB
	InstallationID string
	Admin          PasswordAdministrator
}

// Observe determines whether a step already completed before repeating an effect.
func (e *AdministratorExecutor) Observe(ctx context.Context, op operation.Operation, step operation.Step) (string, bool, error) {
	if op.Type != "setup" || op.OriginatingAction != "setup.mutate" {
		return "", false, ErrSetupConflict
	}
	var ready bool
	err := e.DB.QueryRowContext(ctx, `SELECT b.credential_ready FROM bootstrap_administrator b JOIN installations i ON i.id=b.installation_id WHERE b.installation_id=$1 AND b.operation_id=$2 AND i.state IN ('bootstrap','configuring')`, e.InstallationID, op.ID).Scan(&ready)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrSetupConflict
	}
	if err != nil {
		return "", false, err
	}
	switch step.Key {
	case "administrator":
		return "Initial administrator recorded.", true, nil
	case "credentials":
		return "Local sign-in credential confirmed.", ready, nil
	default:
		return "", false, ErrSetupConflict
	}
}

// Apply calls Dex only after the worker has committed active-step intent.
func (e *AdministratorExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	evidence, complete, err := e.Observe(ctx, op, step)
	if err != nil || complete {
		return evidence, err
	}
	if err := ReconcileAdministrator(ctx, e.DB, e.InstallationID, e.Admin); err != nil {
		return "", err
	}
	return "Local sign-in credential confirmed.", nil
}

// FinishAdministrator reconciles the two durable initial-administrator steps.
// Setup HTTP and the local CLI use the same journal and credential executor.
func FinishAdministrator(ctx context.Context, db *sql.DB, journal *operation.Journal, id string, admin PasswordAdministrator, op operation.Operation) (operation.Operation, error) {
	worker := operation.Worker{Journal: journal, Name: "initial-administrator", LeaseDuration: time.Minute, Executor: &AdministratorExecutor{DB: db, InstallationID: id, Admin: admin}}
	for count := 0; count < 2 && op.Status != "succeeded"; count++ {
		if err := worker.RunOnce(ctx, op.ID); err != nil {
			return operation.Operation{}, err
		}
		var err error
		op, err = journal.Get(ctx, op.ID)
		if err != nil {
			return operation.Operation{}, err
		}
	}
	if op.Status != "succeeded" {
		return operation.Operation{}, ErrSetupConflict
	}
	return op, nil
}
