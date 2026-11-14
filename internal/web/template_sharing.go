package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/template"
)

type templateChange struct {
	Name  *string `json:"name"`
	State *string `json:"state"`
}

// TemplateUpdateHandler changes a template's name and archival state.
func TemplateUpdateHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPatch, Path: "/templates/{templateId}", OperationID: "updateTemplate", Resolver: "template_update"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			resource, err := loadTemplateResource(ctx, tx, r.PathValue("templateId"))
			if err != nil {
				return AuthorizationTarget{}, err
			}
			var change templateChange
			if err := DecodeJSON(nil, r, &change, 4096); err != nil {
				return AuthorizationTarget{}, err
			}
			if change.Name != nil && strings.ContainsRune(*change.Name, 0) {
				return AuthorizationTarget{}, invalidAuthorizationFields()
			}
			target, err := TemplateChangeTarget(resource, change.Name, change.State)
			target.templateChange = &change
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			version, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			change := target.templateChange
			stamp := now()
			view, err := template.Update(ctx, tx, target.Resource.ID, version, change.Name, change.State, stamp)
			if errors.Is(err, template.ErrVersionMismatch) {
				return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the template before saving changes"}
			}
			if err != nil {
				return nil, err
			}
			if change.State != nil && *change.State != target.Resource.State {
				if _, err := audit.Append(ctx, tx, audit.Event{
					OccurredAt: stamp, ActorType: "user", ActorID: actor.User.ID, Action: target.Action,
					ResourceType: "template", ResourceID: view.ID, Outcome: "allowed", ReasonCode: "state_changed",
				}); err != nil {
					return nil, err
				}
			}
			return templateResponse(view, http.StatusOK), nil
		},
	}
}

// TemplateGrantHandlers manages acknowledged launch grants for one template.
func TemplateGrantHandlers(db *sql.DB, origin string, now func() time.Time) (AuthorizedHandler, AuthorizedHandler) {
	grant := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/templates/{templateId}/grants", OperationID: "grantTemplateLaunch", Action: "template.share"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
			resource, err := loadTemplateResource(ctx, tx, r.PathValue("templateId"))
			return AuthorizationTarget{Action: "template.share", Resource: resource}, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var input struct {
				UserID      string `json:"userId"`
				Acknowledge bool   `json:"acknowledgeProjectSecretExposure"`
			}
			if err := DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			if !ValidUUID(input.UserID) || !input.Acknowledge {
				return nil, Problem{Status: 400, Code: "template.grant_invalid", Title: "Choose a user and acknowledge project secret access"}
			}
			key, err := IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			err = template.GrantLaunch(ctx, tx, target.Resource.ID, input.UserID, actor.User.ID, key, now())
			switch {
			case errors.Is(err, template.ErrUnavailable):
				return nil, Problem{Status: 409, Code: "template.grant_unavailable", Title: "Choose an active template and user"}
			case errors.Is(err, template.ErrRequestConflict):
				return nil, Problem{Status: 409, Code: "request.idempotency_conflict", Title: "This request identifier was already used for a different change"}
			case err != nil:
				return nil, err
			}
			return noContent(), nil
		},
	}
	revoke := grant
	revoke.Route = ManagementRoute{Method: http.MethodDelete, Path: "/templates/{templateId}/grants/{userId}", OperationID: "revokeTemplateLaunch", Action: "template.share"}
	revoke.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		user := r.PathValue("userId")
		if !ValidUUID(user) {
			return nil, invalidAuthorizationFields()
		}
		if err := template.RevokeLaunch(ctx, tx, target.Resource.ID, user, actor.User.ID, now()); err != nil {
			return nil, err
		}
		return noContent(), nil
	}
	return grant, revoke
}

func noContent() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}
