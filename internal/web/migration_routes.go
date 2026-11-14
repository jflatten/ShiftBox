package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/release"
)

const migrationPreflightAction = "migration.preflight"

// MigrationPreflightRoute admits authorized checks and returns their durable worker.
func MigrationPreflightRoute(db *sql.DB, journal *operation.Journal, origin, installation string, now func() time.Time,
	evaluate func(context.Context, string) (release.MigrationReport, error)) (AuthorizedHandler, *operation.Worker) {
	handler := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/admin/migrations/preflight", OperationID: "runMigrationPreflight", Action: migrationPreflightAction},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: migrationPreflightAction, Resource: auth.Resource{Type: "installation", ID: installation}}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return admitMigrationPreflight(ctx, tx, r, actor.User.ID, installation, journal)
		},
	}
	worker := &operation.Worker{Journal: journal, Name: "migration-preflight", LeaseDuration: time.Minute,
		Actions: []string{migrationPreflightAction}, Executor: migrationPreflightExecutor{db, installation, evaluate}}
	return handler, worker
}

func admitMigrationPreflight(ctx context.Context, tx *sql.Tx, r *http.Request, actor, installation string, journal *operation.Journal) (http.Handler, error) {
	key, err := IdempotencyKey(r)
	if err != nil {
		return nil, err
	}
	var input struct {
		ReleaseID string `json:"releaseId"`
	}
	if err := DecodeJSON(nil, r, &input, 4096); err != nil {
		return nil, err
	}
	if !release.ValidMigrationReleaseID(input.ReleaseID) {
		return nil, Problem{Status: 400, Code: "migration.invalid_release", Title: "Enter a valid local release ID"}
	}
	// Serialize admissions before looking up the actor's idempotency key.
	result, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE id=$1 AND state IN ('active','blocked')", installation)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return nil, errors.Join(err, operation.ErrConflict)
	}
	op, err := journal.CreateInTx(ctx, tx, operation.Request{
		Actor: actor, Key: key, Type: "update", Generation: 1, OriginatingAction: migrationPreflightAction,
		Steps: []operation.Step{{Key: "preflight", Label: "Check migration prerequisites"}},
	})
	if err != nil {
		return nil, err
	}
	conflict := Problem{Status: 409, Code: "migration.request_conflict", Title: "This request identifier is already in use"}
	if op.OriginatingAction != migrationPreflightAction || op.Type != "update" || op.WorkspaceID != nil {
		return nil, conflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO migration_preflights(operation_id,installation_id,release_id)
		VALUES($1,$2,$3) ON CONFLICT(operation_id) DO NOTHING`, op.ID, installation, input.ReleaseID); err != nil {
		return nil, err
	}
	var matching bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_preflights
		WHERE operation_id=$1 AND installation_id=$2 AND release_id=$3)`, op.ID, installation, input.ReleaseID).Scan(&matching); err != nil {
		return nil, err
	}
	if !matching {
		return nil, conflict
	}
	return noticeJSON(http.StatusAccepted, op, ""), nil
}

type migrationPreflightExecutor struct {
	db           *sql.DB
	installation string
	evaluate     func(context.Context, string) (release.MigrationReport, error)
}

func (e migrationPreflightExecutor) Observe(context.Context, operation.Operation, operation.Step) (string, bool, error) {
	// Checks do not apply a migration. Retrying can safely evaluate them again.
	return "", false, nil
}

func (e migrationPreflightExecutor) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	if op.OriginatingAction != migrationPreflightAction || step.Key != "preflight" {
		return "", operation.ErrConflict
	}
	var releaseID string
	err := e.db.QueryRowContext(ctx, `SELECT p.release_id FROM migration_preflights p
		JOIN installations i ON i.id=p.installation_id JOIN users u ON u.id=$3
		WHERE p.operation_id=$1 AND i.id=$2 AND i.state IN ('active','blocked')
		AND u.role='administrator' AND u.membership_state='active'`, op.ID, e.installation, op.Actor).Scan(&releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &operation.StepFailure{Detail: "Migration preflight authorization is no longer active."}
	}
	if err != nil {
		return "", err
	}
	report, err := e.evaluate(ctx, releaseID)
	if err != nil {
		return "", &operation.StepFailure{Detail: "Check the pinned local release manifest and active installation before retrying."}
	}
	if !report.Ready {
		var detail strings.Builder
		for _, blocker := range report.Blockers {
			fmt.Fprintf(&detail, "%s %s\n", blocker.Code, blocker.Detail)
		}
		return "", &operation.StepFailure{Detail: strings.TrimSpace(detail.String())}
	}
	return "Migration preflight passed.", nil
}
