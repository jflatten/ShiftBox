package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	profileStep     = "validate_sqlite_profile"
	profileEvidence = "Local SQLite data profile selected."
)

func (s SetupHTTP) profileHandler(preflight func(context.Context, database.PostgresSetupTarget) error) http.Handler {
	return s.setupMutation(func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		key, err := web.IdempotencyKey(r)
		if err != nil {
			return nil, err
		}
		var input setupProfileInput
		if err := web.DecodeJSON(nil, r, &input, 16<<10); err != nil {
			return nil, err
		}
		if input.Profile == "postgres" {
			return s.selectPostgresProfile(ctx, tx, r, p, key, input, preflight)
		}
		if input.Profile != "sqlite" || input.ConnectionURI != "" || input.SetupConnectionURI != nil || input.TLSMode != "" {
			return nil, invalidProfile()
		}
		var currentProfile string
		if err := tx.QueryRowContext(ctx, "SELECT data_profile FROM installations WHERE id=$1", s.InstallationID).Scan(&currentProfile); err != nil {
			return nil, err
		}
		if currentProfile != "sqlite" {
			return nil, web.Problem{Status: 409, Code: "setup.profile_conflict", Title: "The installation cannot select local SQLite"}
		}
		var previousID string
		err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", p.User.ID, key).Scan(&previousID)
		if err == nil {
			if _, err := s.readPendingPostgres(ctx, tx); !errors.Is(err, sql.ErrNoRows) {
				return nil, profileConflict()
			}
			op, err := s.Journal.GetInTx(ctx, tx, previousID)
			if err != nil {
				return nil, err
			}
			if len(op.Steps) != 1 || op.Steps[0].Key != profileStep || op.Status != "succeeded" {
				return nil, profileConflict()
			}
			return profileResponse(op), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		op, err := s.Journal.RecordLocalStepInTx(ctx, tx, operation.Request{
			Actor: p.User.ID, Key: key, Type: "setup", OriginatingAction: "setup.mutate", Generation: 1,
			Steps: []operation.Step{{Key: profileStep, Label: "Validate local SQLite profile"}},
		}, profileEvidence)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM protected_values WHERE id=$1", s.pendingPostgresContext().RecordID); err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE installations SET state='configuring', version=version+1, updated_at=$1
				WHERE id=$2 AND state IN ('bootstrap','configuring') AND data_profile='sqlite'`, s.Now().UTC().Format(time.RFC3339Nano), s.InstallationID)
		if err != nil {
			return nil, err
		}
		return profileResponse(op), nil
	})
}

func profileResponse(op operation.Operation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeSetupJSON(w, http.StatusAccepted, op) })
}

func invalidProfile() web.Problem {
	return web.Problem{Status: 400, Code: "setup.invalid_profile", Title: "Select a valid data profile"}
}

func profileConflict() web.Problem {
	return web.Problem{Status: 409, Code: "setup.profile_conflict", Title: "Use a new request key to select the data profile"}
}

func (s SetupHTTP) setupMutation(apply func(context.Context, *sql.Tx, *http.Request, auth.Principal, web.AuthorizationTarget, auth.Decision) (http.Handler, error)) http.Handler {
	return s.setupMethod(http.MethodPut, apply)
}

func (s SetupHTTP) setupMethod(method string, apply func(context.Context, *sql.Tx, *http.Request, auth.Principal, web.AuthorizationTarget, auth.Decision) (http.Handler, error)) http.Handler {
	return &web.AuthorizedHandler{
		DB: s.DB, Origin: s.Origin, Now: s.Now,
		Route:  web.ManagementRoute{Method: method, Action: "setup.mutate"},
		Lookup: s.bootstrapPrincipal,
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "setup.mutate"}, nil
		},
		Apply: apply,
	}
}

func (s SetupHTTP) bootstrapPrincipal(ctx context.Context, tx *sql.Tx, token string) (auth.Principal, error) {
	principal, err := auth.NewStore(s.DB).Principal(ctx, tx, token)
	if err != nil {
		return principal, err
	}
	if principal.Session.Mode != "bootstrap_restricted" || principal.User.Role != "administrator" ||
		(principal.InstallationState != "bootstrap" && principal.InstallationState != "configuring") {
		return auth.Principal{}, sql.ErrNoRows
	}
	var owner string
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM bootstrap_administrator WHERE installation_id=$1", s.InstallationID).Scan(&owner); err != nil {
		return auth.Principal{}, err
	}
	if owner != principal.User.ID {
		return auth.Principal{}, sql.ErrNoRows
	}
	principal.LocalBootstrap = true
	return principal, nil
}
