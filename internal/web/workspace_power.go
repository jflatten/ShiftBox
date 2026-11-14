package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// WorkspacePowerHandler admits start, stop, and rebuild for the workspace owner
// or administrator through the contextual workspace action route.
func WorkspacePowerHandler(db *sql.DB, origin string, now func() time.Time, journal *operation.Journal) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/workspaces/{workspaceId}/actions/{action}", OperationID: "requestWorkspaceAction", Resolver: "workspace_action"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal) (AuthorizationTarget, error) {
			target, err := resolveWorkspaceView(ctx, tx, r, actor)
			if err != nil {
				return AuthorizationTarget{}, err
			}
			action := r.PathValue("action")
			if action != "start" && action != "stop" && action != "rebuild" && action != "repair" {
				return AuthorizationTarget{}, Problem{Status: 400, Code: "workspace.invalid_action", Title: "Choose a supported workspace action"}
			}
			return (WorkspaceAuthorization{Workspace: target.Resource}).Action(actor, action)
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			if journal == nil {
				return nil, errors.New("workspace power actions are incompletely wired")
			}
			version, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			key, err := IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			if r.ContentLength != 0 {
				var input struct {
					ConfirmDataLoss bool `json:"confirmDataLoss"`
				}
				if err := DecodeJSON(nil, r, &input, 4096); err != nil {
					return nil, err
				}
			}
			op, err := workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{
				WorkspaceID: target.Resource.ID, ActorID: actor.User.ID, RequestKey: key,
				Action: r.PathValue("action"), Version: version,
			}, now())
			if err != nil {
				return nil, powerAdmissionProblem(err)
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(op)
			}), nil
		},
	}
}

func powerAdmissionProblem(err error) error {
	switch {
	case errors.Is(err, workspace.ErrPowerConflict):
		return Problem{Status: 409, Code: "workspace.action_conflict", Title: "This request identifier is already in use"}
	case errors.Is(err, workspace.ErrVersionMismatch):
		return Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh before making this change"}
	case errors.Is(err, workspace.ErrOperationInProgress):
		return Problem{Status: 409, Code: "workspace.operation_in_progress", Title: "Wait for the current workspace operation"}
	case errors.Is(err, provider.ErrInvalidState), errors.Is(err, workspace.ErrCapacityConflict):
		return Problem{Status: 409, Code: "workspace.action_unavailable", Title: "This workspace action is unavailable"}
	}
	return launchAdmissionProblem(err) // Both launch and start can exhaust the same capacity budget.
}
