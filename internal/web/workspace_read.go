package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
)

type workspaceView struct {
	ID                 string             `json:"id"`
	OwnerID            string             `json:"ownerId"`
	TemplateID         string             `json:"templateId"`
	TemplateRevision   int64              `json:"templateRevision"`
	PlatformRelease    string             `json:"platformRelease"`
	DesiredState       string             `json:"desiredState"`
	ObservedState      string             `json:"observedState"`
	Generation         int64              `json:"generation"`
	Resources          workspaceResources `json:"resources"`
	CurrentOperationID *string            `json:"currentOperationId"`
	LastActivityAt     *string            `json:"lastActivityAt"`
	RetentionDeadline  *string            `json:"retentionDeadline"`
	Version            int64              `json:"version"`
}

type workspaceResources struct {
	CPUCount  int64 `json:"cpuCount"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskMiB   int64 `json:"diskMiB"`
}

const workspaceProjection = `w.id,w.owner_user_id,w.template_id,w.template_revision,a.release_id,
	w.desired_state,w.observed_state,w.generation,w.cpu_count,w.memory_mib,w.disk_mib,
	(SELECT o.id FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
	   AND o.status IN ('pending','running','waiting') ORDER BY o.created_at DESC,o.id DESC LIMIT 1),
	w.last_activity_at,w.retention_deadline,w.version`

const workspaceFrom = ` FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id`

// WorkspaceReadHandlers returns the list and detail operations for management.
func WorkspaceReadHandlers(db *sql.DB, origin string, now func() time.Time) (AuthorizedHandler, AuthorizedHandler) {
	store := auth.NewStore(db)
	list := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/workspaces", OperationID: "listWorkspaces", Action: "workspace.list"},
		Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "workspace.list", Resource: auth.Resource{Type: "workspace", OwnerID: principal.User.ID}}, nil
		},
		Apply: listWorkspaces,
	}
	detail := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodGet, Path: "/workspaces/{workspaceId}", OperationID: "getWorkspace", Action: "workspace.view"},
		Resolve: resolveWorkspaceView,
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			view, err := scanWorkspace(tx.QueryRowContext(ctx, `SELECT `+workspaceProjection+workspaceFrom+` WHERE w.id=$1`, target.Resource.ID))
			if err != nil {
				return nil, err
			}
			resource, _, err := serviceWorkspace(ctx, tx, view.ID, "ide")
			if err != nil {
				return nil, err
			}
			entry, err := serviceEntryTarget(ctx, tx, store, principal, resource)
			if err != nil {
				return nil, err
			}
			// Expose the current entry decision without issuing a grant.
			response := struct {
				workspaceView
				CanOpenIDE bool `json:"canOpenIde"`
			}{view, auth.Authorize(principal, entry.Action, entry.Resource, entry.Grants, now()).Allowed}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}), nil
		},
	}
	return list, detail
}

func resolveWorkspaceView(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
	id := r.PathValue("workspaceId")
	if !ValidUUID(id) {
		return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid workspace identifier"}
	}
	resource := auth.Resource{Type: "workspace", ID: id}
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM workspaces WHERE id=$1`, id).Scan(&resource.OwnerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && resource.OwnerID != principal.User.ID && principal.User.Role != "administrator") {
		return AuthorizationTarget{}, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	return AuthorizationTarget{Action: "workspace.view", Resource: resource}, nil
}

func listWorkspaces(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	query := r.URL.Query()
	owner, state, cursor := query.Get("owner"), query.Get("state"), query.Get("cursor")
	states := []string{"unknown", "provisioning", "stopped", "starting", "running", "unreachable", "stopping", "deleting", "deleted", "failed"}
	if (owner != "" && !ValidUUID(owner)) || (cursor != "" && !ValidUUID(cursor)) ||
		(state != "" && !slices.Contains(states, state)) {
		return nil, Problem{Status: 400, Code: "request.invalid_filter", Title: "Check the workspace filters"}
	}
	const limit = 50
	admin := principal.User.Role == "administrator"
	rows, err := tx.QueryContext(ctx, `SELECT `+workspaceProjection+workspaceFrom+`
		WHERE ($1 OR w.owner_user_id=$2) AND ($3='' OR w.owner_user_id=$3)
		  AND ($4='' OR w.observed_state=$4) AND w.id>$5 ORDER BY w.id LIMIT $6`,
		admin, principal.User.ID, owner, state, cursor, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Items      []workspaceView `json:"items"`
		NextCursor *string         `json:"nextCursor"`
	}{Items: make([]workspaceView, 0, limit)}
	for rows.Next() {
		view, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		if len(result.Items) == limit {
			result.NextCursor = &result.Items[limit-1].ID
			break
		}
		result.Items = append(result.Items, view)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}), nil
}

func scanWorkspace(row interface{ Scan(...any) error }) (workspaceView, error) {
	var view workspaceView
	err := row.Scan(&view.ID, &view.OwnerID, &view.TemplateID, &view.TemplateRevision, &view.PlatformRelease,
		&view.DesiredState, &view.ObservedState, &view.Generation,
		&view.Resources.CPUCount, &view.Resources.MemoryMiB, &view.Resources.DiskMiB,
		&view.CurrentOperationID, &view.LastActivityAt, &view.RetentionDeadline, &view.Version)
	return view, err
}
