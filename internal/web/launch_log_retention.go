package web

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/launchlog"
)

// LaunchLogRetentionHandler updates the administrator's launch diagnostics policy.
func LaunchLogRetentionHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	const action = "infrastructure.manage"
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPut, Path: "/admin/launch-log-retention", OperationID: "updateLaunchLogRetention", Action: action},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: action}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			version, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				Days int `json:"days"`
			}
			if err := DecodeJSON(nil, r, &input, 1024); err != nil {
				return nil, err
			}
			if !launchlog.ValidRetentionDays(input.Days) {
				return nil, Problem{Status: 400, Code: "settings.invalid_launch_log_retention", Title: "Enter between 1 and 3,650 days"}
			}
			result, err := tx.ExecContext(ctx, `UPDATE installations SET launch_log_retention_days=$1,
				launch_log_retention_version=launch_log_retention_version+1
				WHERE id=$2 AND state='active' AND launch_log_retention_version=$3`, input.Days, actor.InstallationID, version)
			if err != nil {
				return nil, err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if count != 1 {
				return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the retention setting before updating"}
			}
			if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now(), ActorType: "user", ActorID: actor.User.ID,
				Action: "settings.launch_log_retention.update", ResourceType: "installation", ResourceID: actor.InstallationID,
				Outcome: "allowed", ReasonCode: "administrator_policy"}); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
		},
	}
}
