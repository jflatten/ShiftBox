package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// KeepAlivePolicyHandler saves the current administrator inactivity policy.
func KeepAlivePolicyHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPut, Path: "/admin/keep-alive", OperationID: "updateKeepAlivePolicy", Action: "infrastructure.manage"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "infrastructure.manage"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			version, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				MaximumMinutes *int64  `json:"maximumMinutes"`
				AllowedMinutes []int64 `json:"allowedMinutes"`
				DefaultMinutes *int64  `json:"defaultMinutes"`
			}
			if err := DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			if input.MaximumMinutes == nil || input.DefaultMinutes == nil {
				return nil, Problem{Status: 400, Code: "infrastructure.invalid_keep_alive", Title: "Enter a maximum, allowed values, and default keep-alive"}
			}
			policy := workspace.KeepAlivePolicy{MaximumMinutes: *input.MaximumMinutes, AllowedMinutes: input.AllowedMinutes, DefaultMinutes: *input.DefaultMinutes}
			if err := policy.Validate(); err != nil {
				return nil, Problem{Status: 400, Code: "infrastructure.invalid_keep_alive", Title: "Choose valid keep-alive values and a default within the maximum"}
			}
			// Policy writes take the same admission lock as lifecycle and shutdown
			// checks, so a concurrent timer cannot commit using stale limits.
			if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
				return nil, err
			}
			var installation string
			if err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&installation); err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(policy.AllowedMinutes)
			if err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(ctx, `UPDATE installations SET keep_alive_maximum_minutes=$1,
				keep_alive_allowed_minutes_json=$2,keep_alive_default_minutes=$3,keep_alive_version=keep_alive_version+1
				WHERE singleton=1 AND state='active' AND keep_alive_version=$4`, policy.MaximumMinutes, string(encoded), policy.DefaultMinutes, version)
			if err != nil {
				return nil, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if count != 1 {
				return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the keep-alive settings before updating"}
			}
			if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now(), ActorType: "user", ActorID: actor.User.ID, Action: "infrastructure.keep_alive.update", ResourceType: "installation", ResourceID: installation, Outcome: "allowed", ReasonCode: "administrator_policy"}); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
		},
	}
}
