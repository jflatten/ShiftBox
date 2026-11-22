package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const restoreValidationAction = "restore.validate"

// RestoreValidationJob contains private request inputs kept only in memory.
type RestoreValidationJob struct {
	OperationID string `json:"-"`
	Destination string `json:"destination"`
	ArchivePath string `json:"archivePath"`
	Password    string `json:"password"`
	Postgres    *struct {
		Profile          string `json:"profile"`
		TLSMode          string `json:"tlsMode"`
		ConnectionURI    string `json:"connectionUri"`
		DexConnectionURI string `json:"dexConnectionUri"`
	} `json:"replacementPostgres"`
}

// RestoreValidationRoute admits the check without retaining archive credentials.
func RestoreValidationRoute(db *sql.DB, journal *operation.Journal, origin, installation string, now func() time.Time,
	enqueue func(RestoreValidationJob)) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/restores/validate", OperationID: "validateRestore", Action: restoreValidationAction},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: restoreValidationAction, Resource: auth.Resource{Type: "installation", ID: installation}}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			key, err := IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			var job RestoreValidationJob
			if err := DecodeJSON(nil, r, &job, 16<<10); err != nil {
				return nil, err
			}
			if !filepath.IsAbs(job.ArchivePath) || strings.ContainsRune(job.ArchivePath, 0) || len(job.ArchivePath) > 4096 ||
				job.Destination != "" && (!filepath.IsAbs(job.Destination) || strings.ContainsRune(job.Destination, 0) || len(job.Destination) > 4096) ||
				job.Password == "" || len(job.Password) > 1024 || job.Postgres != nil &&
				(job.Postgres.Profile != "postgres" || job.Postgres.TLSMode != "verify-full" || job.Postgres.ConnectionURI == "" || job.Postgres.DexConnectionURI == "") {
				return nil, Problem{Status: 400, Code: "restore.invalid_input", Title: "Provide the archive passphrase and both scoped PostgreSQL logins when applicable"}
			}
			result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state='active'", installation)
			if err != nil {
				return nil, err
			}
			conflict := Problem{Status: 409, Code: "restore.validation_conflict", Title: "Wait for the current validation or use a new request identifier"}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return nil, conflict
			}
			var existingID string
			err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", actor.User.ID, key).Scan(&existingID)
			if err == nil {
				op, err := journal.GetInTx(ctx, tx, existingID)
				if err == nil && op.OriginatingAction != restoreValidationAction {
					return nil, conflict
				}
				return noticeJSON(http.StatusAccepted, op, ""), err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			var busy bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE originating_action=$1 AND status IN ('pending','running','waiting'))`,
				restoreValidationAction).Scan(&busy); err != nil {
				return nil, err
			}
			if busy {
				return nil, conflict
			}
			op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: actor.User.ID, Key: key, Type: "restore", Generation: 1,
				OriginatingAction: restoreValidationAction, Steps: []operation.Step{{Key: "validate", Label: "Verify archive and replacement"}}})
			if err != nil {
				return nil, err
			}
			job.OperationID = op.ID
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				enqueue(job)
				noticeJSON(http.StatusAccepted, op, "").ServeHTTP(w, r)
			}), nil
		},
	}
}

// InterruptRestoreValidations closes operations whose inputs were lost on restart.
func InterruptRestoreValidations(ctx context.Context, db *sql.DB, journal *operation.Journal) error {
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE originating_action=$1
			AND status IN ('pending','running','waiting')`, restoreValidationAction).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return journal.InterruptInTx(ctx, tx, id, "Validation was interrupted. Submit the archive and passphrase again.")
	})
}

// RunRestoreValidations manages the lifetime of in-memory validation jobs.
func RunRestoreValidations(ctx context.Context, db *sql.DB, journal *operation.Journal, jobs <-chan RestoreValidationJob,
	validate func(context.Context, RestoreValidationJob) (string, error)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job, ok := <-jobs:
			if !ok {
				return nil
			}
			if err := runRestoreValidation(ctx, db, journal, job, validate); err != nil {
				return err
			}
		}
	}
}

func runRestoreValidation(ctx context.Context, db *sql.DB, journal *operation.Journal, job RestoreValidationJob,
	validate func(context.Context, RestoreValidationJob) (string, error)) error {
	claim, err := journal.Claim(ctx, job.OperationID, "restore-validation", 30*time.Minute)
	if err != nil {
		return err
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		return err
	}
	effect, cancel := context.WithTimeout(ctx, 29*time.Minute)
	id, err := validate(effect, job)
	cancel()
	record, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err == nil && ValidUUID(id) {
		return journal.CompleteStep(record, claim, 0, id)
	}
	return database.Transaction(record, db, func(tx *sql.Tx) error {
		return journal.FailStepInTx(record, tx, claim, 0, "Restore validation failed. Check the archive, passphrase, installation key and replacement target.")
	})
}
