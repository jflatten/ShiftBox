package app

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	profileStep     = "validate_sqlite_profile"
	profileEvidence = "Local SQLite data profile selected."
)

func (s SetupHTTP) profileHandler() http.Handler {
	return &web.AuthorizedHandler{
		DB: s.DB, Origin: s.Origin, Now: s.Now,
		Route:  web.ManagementRoute{Method: http.MethodPut, Action: "setup.mutate"},
		Lookup: s.bootstrapPrincipal,
		Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, _ auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "setup.mutate"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			key, err := web.IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				Profile            string  `json:"profile"`
				ConnectionURI      string  `json:"connectionUri"`
				SetupConnectionURI *string `json:"setupConnectionUri"`
				TLSMode            string  `json:"tlsMode"`
			}
			// Decode within admission so rejected requests cannot create operation intent.
			if err := web.DecodeJSON(nil, r, &input, 16<<10); err != nil {
				return nil, err
			}
			if input.Profile == "postgres" {
				return nil, web.Problem{Status: 501, Code: "setup.postgres_unavailable", Title: "PostgreSQL preflight is unavailable"}
			}
			if input.Profile != "sqlite" || input.ConnectionURI != "" || input.SetupConnectionURI != nil || input.TLSMode != "" {
				return nil, web.Problem{Status: 400, Code: "setup.invalid_profile", Title: "Select a valid data profile"}
			}
			var currentProfile string
			if err := tx.QueryRowContext(ctx, "SELECT data_profile FROM installations WHERE id=$1", s.InstallationID).Scan(&currentProfile); err != nil {
				return nil, err
			}
			if currentProfile != "sqlite" {
				return nil, web.Problem{Status: 409, Code: "setup.profile_conflict", Title: "The installation cannot select local SQLite"}
			}
			op, err := s.Journal.RecordLocalStepInTx(ctx, tx, operation.Request{
				Actor: p.User.ID, Key: key, Type: "setup", OriginatingAction: "setup.mutate", Generation: 1,
				Steps: []operation.Step{{Key: profileStep, Label: "Validate local SQLite profile"}},
			}, profileEvidence)
			if err != nil {
				return nil, err
			}
			_, err = tx.ExecContext(ctx, `UPDATE installations SET state='configuring', version=version+1, updated_at=$1
				WHERE id=$2 AND state='bootstrap' AND data_profile='sqlite'`, s.Now().UTC().Format(time.RFC3339Nano), s.InstallationID)
			if err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeSetupJSON(w, http.StatusAccepted, op) }), nil
		},
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
