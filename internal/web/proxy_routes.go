package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

const serviceEntryPath = "/_shiftbox/entry"

// WorkspaceEntryGrantHandler issues a short-lived exchange URL for one
// service after the normal management session, CSRF and policy checks.
func WorkspaceEntryGrantHandler(db *sql.DB, origin string, now func() time.Time, hosts *config.ServiceHosts) *AuthorizedHandler {
	store := auth.NewStore(db)
	return &AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/workspaces/{workspaceId}/entry-grants",
			OperationID: "createWorkspaceEntryGrant", Resolver: "workspace_entry"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			var input struct {
				Service string `json:"service"`
			}
			if err := DecodeJSON(nil, r, &input, 1024); err != nil {
				return AuthorizationTarget{}, err
			}
			workspace := r.PathValue("workspaceId")
			host, err := hosts.Hostname(workspace, input.Service)
			if err != nil {
				return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_service", Title: "Invalid workspace service"}
			}
			resource, generation, err := serviceWorkspace(ctx, tx, workspace, input.Service)
			if errors.Is(err, sql.ErrNoRows) {
				return AuthorizationTarget{}, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
			}
			if err != nil {
				return AuthorizationTarget{}, err
			}
			target := AuthorizationTarget{Action: "workspace.connect_owner", Resource: resource,
				entry: &proxy.OriginScope{WorkspaceID: workspace, Generation: generation, Service: input.Service, Origin: "https://" + host}}
			if resource.OwnerID != principal.User.ID {
				target.Action = "workspace.connect_admin"
				target.Grants, err = store.ActiveGrants(ctx, tx, principal.User.ID, "workspace", workspace)
			}
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			binding := proxy.OriginAuthority{Scope: *target.entry, UserID: principal.User.ID,
				SessionID: principal.Session.ID, Permission: target.Action}
			if target.Action == "workspace.connect_admin" {
				for _, grant := range target.Grants {
					if grant.Action == target.Action {
						binding.ResourceGrantID = grant.ID
						break
					}
				}
			}
			token, expiry, err := proxy.IssueOriginGrant(ctx, tx, binding, now())
			if err != nil {
				return nil, err
			}
			result := struct {
				ExchangeURL string    `json:"exchangeUrl"`
				ExpiresAt   time.Time `json:"expiresAt"`
			}{binding.Scope.Origin + serviceEntryPath + "?" + url.Values{"grant": {token}}.Encode(), expiry}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(result)
			}), nil
		},
	}
}

func serviceWorkspace(ctx context.Context, tx *sql.Tx, workspace, service string) (auth.Resource, int64, error) {
	resource := auth.Resource{Type: "workspace", ID: workspace}
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT w.owner_user_id,w.generation,
		(w.desired_state='running' AND w.observed_state='running' AND i.state='active'
		AND owner.membership_state='active' AND ($2<>'ide' OR EXISTS (
		 SELECT 1 FROM operations o JOIN operation_steps s ON s.operation_id=o.id
		 WHERE o.workspace_id=w.id AND o.generation=w.generation AND s.step_key='verify_ide' AND s.status='succeeded'
		)))
		FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN users owner ON owner.id=w.owner_user_id
		WHERE w.id=$1`, workspace, service).Scan(&resource.OwnerID, &generation, &resource.Eligible)
	return resource, generation, err
}
