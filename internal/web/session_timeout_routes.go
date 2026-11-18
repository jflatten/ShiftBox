package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
)

// SessionTimeoutHandlers read and update the administrator's session timeout.
func SessionTimeoutHandlers(db *sql.DB, origin string, now func() time.Time) (AuthorizedHandler, AuthorizedHandler) {
	const path = "/admin/settings/session"
	const action = "infrastructure.manage"
	read := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: path, OperationID: "getSessionTimeout", Action: action},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: action}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			timeout, err := auth.ReadSessionTimeout(ctx, tx, actor.InstallationID)
			if err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(timeout)
			}), nil
		},
	}
	update := read
	update.Route = ManagementRoute{Method: http.MethodPut, Path: path, OperationID: "updateSessionTimeout", Action: action}
	update.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		version, err := RequireVersion(r)
		if err != nil {
			return nil, err
		}
		var input struct {
			Value int64  `json:"value"`
			Unit  string `json:"unit"`
		}
		if err := DecodeJSON(nil, r, &input, 4096); err != nil {
			return nil, err
		}
		timeout := auth.SessionTimeout{Value: input.Value, Unit: input.Unit}
		if _, valid := timeout.Duration(); !valid {
			return nil, Problem{Status: 400, Code: "settings.invalid_session_timeout", Title: "Enter a positive whole number and a time unit, up to 400 days"}
		}
		result, err := tx.ExecContext(ctx, `UPDATE installations SET session_timeout_value=$1,session_timeout_unit=$2,
			session_timeout_version=session_timeout_version+1 WHERE id=$3 AND state='active' AND session_timeout_version=$4`,
			timeout.Value, timeout.Unit, actor.InstallationID, version)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the session timeout before updating"}
		}
		if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now(), ActorType: "user", ActorID: actor.User.ID,
			Action: "settings.session_timeout.update", ResourceType: "installation", ResourceID: actor.InstallationID,
			Outcome: "allowed", ReasonCode: "administrator_policy"}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
	}
	return read, update
}
