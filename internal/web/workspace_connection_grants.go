package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

const manageConnectionGrants = "workspace.manage_connection_grants"
const connectionGrantPath = "/admin/workspaces/{workspaceId}/connection-grants"

type connectionGrantView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// WorkspaceConnectionGrantHandlers lists and revokes existing administrator
// connection authority within a single workspace.
func WorkspaceConnectionGrantHandlers(db *sql.DB, origin string, now func() time.Time, registry *proxy.Registry) (AuthorizedHandler, AuthorizedHandler) {
	list := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: connectionGrantPath, OperationID: "listWorkspaceConnectionGrants", Action: manageConnectionGrants},
		Resolve: func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			id := r.PathValue("workspaceId")
			if !ValidUUID(id) {
				return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid workspace identifier"}
			}
			return AuthorizationTarget{Action: manageConnectionGrants, Resource: auth.Resource{Type: "workspace", ID: id}}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=$1)", target.Resource.ID).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
			}
			rows, err := tx.QueryContext(ctx, `SELECT g.id,u.primary_email FROM resource_grants g JOIN users u ON u.id=g.subject_user_id
				WHERE g.resource_type='workspace' AND g.resource_id=$1 AND g.action='workspace.connect_admin' AND g.revoked_at IS NULL
				ORDER BY u.primary_email,g.id`, target.Resource.ID)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			items := []connectionGrantView{}
			for rows.Next() {
				var item connectionGrantView
				if err := rows.Scan(&item.ID, &item.Email); err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(struct {
					Items []connectionGrantView `json:"items"`
				}{Items: items})
			}), nil
		},
	}
	revoke := list
	revoke.Route = ManagementRoute{Method: http.MethodDelete, Path: connectionGrantPath + "/{grantId}", OperationID: "revokeWorkspaceConnectionGrant", Action: manageConnectionGrants}
	revoke.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		id, workspace := r.PathValue("grantId"), target.Resource.ID
		if !ValidUUID(id) {
			return nil, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid connection grant identifier"}
		}
		if registry == nil {
			return nil, errors.New("connection registry is unavailable")
		}
		if err := revokeConnectionGrant(ctx, tx, id, workspace, actor.User.ID, now()); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := registry.CloseMatching(func(scope proxy.Scope) bool {
				return scope.GrantID == id && scope.WorkspaceID == workspace
			}); err != nil {
				WriteProblem(w, RequestID(r.Context()), Problem{Status: 503, Code: "workspace.connection_closure_pending",
					Title: "The grant is revoked. Retry to finish closing its connections.", Retryable: true})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}
	return list, revoke
}

func revokeConnectionGrant(ctx context.Context, tx *sql.Tx, id, workspace, actor string, now time.Time) error {
	const scope = `id=$1 AND resource_id=$2 AND resource_type='workspace' AND action='workspace.connect_admin'`
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE resource_grants SET revoked_at=$3 WHERE revoked_at IS NULL AND `+scope, id, workspace, stamp)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_grants WHERE `+scope+`)`, id, workspace).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return Problem{Status: 404, Code: "workspace.connection_grant_not_found", Title: "Connection grant not found"}
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE origin_grants SET revoked_at=$3
		WHERE resource_grant_id=$1 AND workspace_id=$2 AND revoked_at IS NULL`, id, workspace, stamp); err != nil {
		return err
	}
	_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: actor,
		Action: manageConnectionGrants, ResourceType: "workspace", ResourceID: workspace, Outcome: "allowed",
		ReasonCode: "connection_grant_revoked", Metadata: audit.Metadata{GrantID: id}})
	return err
}
