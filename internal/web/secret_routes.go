package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/secret"
)

// SecretHandlers binds metadata reads and write-only mutations to live scope authority.
func SecretHandlers(db *sql.DB, origin string, service *secret.Service, now func() time.Time) map[string]AuthorizedHandler {
	handlers := make(map[string]AuthorizedHandler)
	if service == nil {
		return handlers
	}
	for _, route := range ManagementRoutes() {
		if route.Resolver != "secret_scope" {
			continue
		}
		handlers[route.OperationID] = AuthorizedHandler{
			DB: db, Origin: origin, Now: now, Route: route, Resolve: resolveSecretScope,
			Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
				scope := secret.Scope{Type: target.Resource.Type, ID: target.Resource.ID}
				if r.Method == http.MethodGet {
					var installation string
					if err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1 AND state='active'").Scan(&installation); err != nil {
						return nil, err
					}
					items, err := service.List(ctx, tx, scope, installation)
					if err != nil {
						return nil, err
					}
					return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(items)
					}), nil
				}
				name := r.PathValue("name")
				if !secret.ValidName(name) {
					return nil, invalidAuthorizationFields()
				}
				switch r.Method {
				case http.MethodPut:
					var input struct {
						Value *string `json:"value"`
					}
					if err := DecodeJSON(nil, r, &input, 64<<10); err != nil {
						return nil, err
					}
					if input.Value == nil {
						return nil, invalidAuthorizationFields()
					}
					value := []byte(*input.Value)
					defer clear(value)
					if err := service.Set(ctx, tx, scope, name, value, principal.User.ID, now()); err != nil {
						return nil, err
					}
				case http.MethodDelete:
					ack := r.Header.Values("X-Acknowledge-Fallback")
					if len(ack) != 1 || ack[0] != "acknowledged" {
						return nil, Problem{Status: 400, Code: "secret.fallback_ack_required", Title: "Acknowledge the fallback before deleting this override"}
					}
					if err := service.Delete(ctx, tx, scope, name, principal.User.ID, now()); err != nil {
						return nil, err
					}
				}
				return noContent(), nil
			},
		}
	}
	return handlers
}

func resolveSecretScope(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
	scope, id := r.PathValue("scope"), r.PathValue("scopeId")
	if r.Method == http.MethodGet {
		scope, id = r.URL.Query().Get("scope"), r.URL.Query().Get("scopeId")
	}
	if !ValidUUID(id) {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	resource := auth.Resource{Type: scope, ID: id}
	var err error
	switch scope {
	case "user":
		err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=$1", id).Scan(&resource.OwnerID)
	case "template":
		resource, err = loadTemplateResource(ctx, tx, id)
	case "organization":
		err = tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE id=$1 AND singleton=1 AND state='active'", id).Scan(&resource.OwnerID)
	default:
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationTarget{}, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	return SecretScopeTarget(scope, id, resource)
}
