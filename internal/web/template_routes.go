package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/template"
)

// TemplateHandlers serves listing, creation, and immutable template revisions.
func TemplateHandlers(db *sql.DB, origin string, now func() time.Time, newID func() string) (list, create, revise AuthorizedHandler) {
	list = AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/templates", OperationID: "listTemplates", Action: "template.view"},
		Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, p auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "template.view", Resource: auth.Resource{Type: "template", State: "active", OwnerID: p.User.ID}}, nil
		},
		Apply: listTemplates,
	}
	create = AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/templates", OperationID: "createTemplate", Action: "template.create"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "template.create"}, nil
		},
		Apply: saveTemplate(now, newID),
	}
	revise = AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodPost, Path: "/templates/{templateId}/revisions", OperationID: "createTemplateRevision", Action: "template.create_revision"},
		Resolve: resolveTemplateRevision,
		Apply:   saveTemplate(now, newID),
	}
	return list, create, revise
}

func resolveTemplateRevision(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
	id := r.PathValue("templateId")
	if !ValidUUID(id) {
		return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid template identifier"}
	}
	resource := auth.Resource{Type: "template", ID: id}
	err := tx.QueryRowContext(ctx, "SELECT owner_user_id,state FROM project_templates WHERE id=$1", id).Scan(&resource.OwnerID, &resource.State)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationTarget{}, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
	}
	return AuthorizationTarget{Action: "template.create_revision", Resource: resource}, err
}

func saveTemplate(now func() time.Time, newID func() string) func(context.Context, *sql.Tx, *http.Request, auth.Principal, AuthorizationTarget, auth.Decision) (http.Handler, error) {
	return func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		intent := template.SaveIntent{Actor: p.User.ID, TemplateID: target.Resource.ID}
		if err := DecodeJSON(nil, r, &intent.Input, 16<<10); err != nil {
			return nil, err
		}
		if !ValidUUID(intent.Input.ProfileID) || !ValidUUID(intent.Input.ArtifactID) ||
			intent.Input.NetworkPolicyID != "" && !ValidUUID(intent.Input.NetworkPolicyID) {
			return nil, Problem{Status: 400, Code: "template.invalid_input", Title: "Check the template settings"}
		}
		key, err := IdempotencyKey(r)
		if err != nil {
			return nil, err
		}
		intent.Key = key
		if intent.TemplateID == "" {
			if newID == nil {
				return nil, errors.New("template identity generator is missing")
			}
			intent.NewID = newID()
		} else {
			intent.Version, err = RequireVersion(r)
			if err != nil {
				return nil, err
			}
		}
		view, err := template.Save(ctx, tx, intent, now())
		switch {
		case errors.Is(err, template.ErrInvalidInput):
			return nil, Problem{Status: 400, Code: "template.invalid_input", Title: "Check the template settings"}
		case errors.Is(err, template.ErrUnavailable):
			return nil, Problem{Status: 409, Code: "template.unavailable", Title: "The template or its launch configuration is unavailable"}
		case errors.Is(err, template.ErrRequestConflict):
			return nil, Problem{Status: 409, Code: "request.idempotency_conflict", Title: "This request identifier was already used for a different change"}
		case errors.Is(err, template.ErrVersionMismatch):
			return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the template before creating a revision"}
		case err != nil:
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(view)
		}), nil
	}
}

func listTemplates(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	query := r.URL.Query()
	cursor, archived := query.Get("cursor"), query.Get("archived")
	if cursor != "" && !ValidUUID(cursor) || archived != "" && archived != "true" && archived != "false" {
		return nil, Problem{Status: 400, Code: "request.invalid_filter", Title: "Check the template filters"}
	}
	admin := p.User.Role == "administrator"
	state := "active"
	if archived == "true" {
		if !admin {
			return nil, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		state = "archived"
	}
	items, next, err := template.List(ctx, tx, p.User.ID, admin, state, cursor)
	if err != nil {
		return nil, err
	}
	response := struct {
		Items      []template.View `json:"items"`
		NextCursor *string         `json:"nextCursor"`
	}{items, next}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}), nil
}
