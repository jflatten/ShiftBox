package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// OperationHandlers returns the management detail and event-stream handlers.
// Both resolve the operation's current scope through the same policy.
func OperationHandlers(db *sql.DB, origin string, now func() time.Time, journal *operation.Journal, registry *proxy.Registry) (AuthorizedHandler, AuthorizedHandler) {
	resolve := func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
		return resolveOperationView(ctx, tx, journal, r.PathValue("operationId"), principal, now())
	}
	detail := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodGet, Path: "/operations/{operationId}", OperationID: "getOperation", Resolver: "operation_resource"},
		Resolve: resolve,
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			op, err := journal.GetInTx(ctx, tx, r.PathValue("operationId"))
			if err != nil {
				return nil, err
			}
			response := struct {
				operation.Operation
				LaunchPerformance *launchlog.Performance `json:"launchPerformance,omitempty"`
			}{Operation: op}
			if op.Type == "provision" && op.OriginatingAction == "workspace.launch" {
				performance, err := launchlog.Read(ctx, tx, op.ID, now())
				if err != nil {
					return nil, err
				}
				response.LaunchPerformance = &performance
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}), nil
		},
	}
	stream := detail
	stream.Route = ManagementRoute{Method: http.MethodGet, Path: "/operations/{operationId}/events", OperationID: "streamOperationEvents", Resolver: "operation_resource"}
	stream.Apply = func(context.Context, *sql.Tx, *http.Request, auth.Principal, AuthorizationTarget, auth.Decision) (http.Handler, error) {
		if registry == nil {
			return nil, errors.New("operation stream registry is unavailable")
		}
		return OperationEvents(journal, registry, func(r *http.Request, id string) (proxy.Scope, error) {
			return authorizeOperationStream(r, db, journal, id, now())
		}, 0), nil
	}
	return detail, stream
}

func resolveOperationView(ctx context.Context, tx *sql.Tx, journal *operation.Journal, id string,
	principal auth.Principal, now time.Time,
) (AuthorizationTarget, error) {
	missing := Problem{Status: 404, Code: "operation.not_found", Title: "Operation not found"}
	if !ValidUUID(id) {
		return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid operation identifier"}
	}
	op, err := journal.GetInTx(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationTarget{}, missing
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	var resource auth.Resource
	if op.WorkspaceID != nil {
		resource.Type, resource.ID = "workspace", *op.WorkspaceID
		err = tx.QueryRowContext(ctx, "SELECT owner_user_id FROM workspaces WHERE id=$1", resource.ID).Scan(&resource.OwnerID)
	} else {
		resource.Type = "installation"
		err = tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&resource.ID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationTarget{}, missing
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	target, err := OperationViewTarget(op, resource)
	if err != nil {
		return AuthorizationTarget{}, missing
	}
	if !auth.Authorize(principal, target.Action, target.Resource, target.Grants, now).Allowed {
		return AuthorizationTarget{}, missing
	}
	return target, nil
}

func authorizeOperationStream(r *http.Request, db *sql.DB, journal *operation.Journal, id string, now time.Time) (proxy.Scope, error) {
	return authorizeManagementStream(r, db, now, func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
		return resolveOperationView(ctx, tx, journal, id, principal, now)
	})
}

func authorizeManagementStream(r *http.Request, db *sql.DB, now time.Time,
	resolve func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error),
) (proxy.Scope, error) {
	token, err := SessionToken(r)
	if err != nil {
		return proxy.Scope{}, err
	}
	var scope proxy.Scope
	err = database.Transaction(r.Context(), db, func(tx *sql.Tx) error {
		ctx := r.Context()
		principal, err := auth.NewStore(db).Principal(ctx, tx, token)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !auth.Authorize(principal, "session.view", auth.Resource{}, nil, now).Allowed) {
			return Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		if err != nil {
			return err
		}
		target, err := resolve(ctx, tx, r, principal)
		if err != nil {
			return err
		}
		if !auth.Authorize(principal, target.Action, target.Resource, target.Grants, now).Allowed {
			return Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		scope = proxy.Scope{UserID: principal.User.ID, SessionID: principal.Session.ID}
		if target.Resource.Type == "workspace" {
			scope.WorkspaceID = target.Resource.ID
		}
		return nil
	})
	return scope, err
}
