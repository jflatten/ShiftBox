package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/template"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type templateCatalogChoice struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderKind string `json:"providerKind,omitempty"`
}

// TemplateCatalogHandler lists the profile and image choices for template authors.
func TemplateCatalogHandler(db *sql.DB, origin string, now func() time.Time, policy WorkspaceLaunchPolicy) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/templates/catalog", OperationID: "listTemplateCatalog", Action: "template.create"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "template.create"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			result := struct {
				ResourceProfiles          []templateCatalogChoice `json:"resourceProfiles"`
				PlatformImages            []templateCatalogChoice `json:"platformImages"`
				DefaultPlatformArtifactID string                  `json:"defaultPlatformArtifactId,omitempty"`
			}{ResourceProfiles: []templateCatalogChoice{}, PlatformImages: []templateCatalogChoice{}}
			if policy.Capabilities.Create && policy.Capabilities.Start {
				var err error
				result.ResourceProfiles, err = templateCatalogProfiles(ctx, tx, policy)
				if err != nil {
					return nil, err
				}
				result.PlatformImages, result.DefaultPlatformArtifactID, err = templateCatalogImages(ctx, tx, policy)
				if err != nil {
					return nil, err
				}
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(result)
			}), nil
		},
	}
}

func templateCatalogProfiles(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy) ([]templateCatalogChoice, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,cpu_count,memory_mib,disk_mib FROM resource_profiles
		WHERE enabled=1 ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	choices := []templateCatalogChoice{}
	for rows.Next() {
		var item templateCatalogChoice
		var allocation workspace.Allocation
		if err := rows.Scan(&item.ID, &item.Name, &allocation.CPU, &allocation.MemoryMiB, &allocation.DiskMiB); err != nil {
			return nil, err
		}
		if _, err := workspace.ProfileResources(allocation, policy.Capabilities); err == nil {
			choices = append(choices, item)
		}
	}
	return choices, rows.Err()
}

func launchProviderKind(caps provider.Capabilities) string {
	if caps.Name == "ec2" {
		return "ec2"
	}
	return "utm"
}

func templateCatalogImages(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy) ([]templateCatalogChoice, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.release_id,a.protocol_version,a.architecture,a.is_default,a.provider_kind FROM platform_artifacts a
		CROSS JOIN installations i WHERE i.singleton=1 AND a.state='available' AND a.provider_kind=$1
		AND a.guest_release IN (i.active_release,i.min_guest_release) ORDER BY a.release_id,a.id`, launchProviderKind(policy.Capabilities))
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	choices := []templateCatalogChoice{}
	var defaultID string
	for rows.Next() {
		var item templateCatalogChoice
		var protocol, architecture string
		var isDefault int
		if err := rows.Scan(&item.ID, &item.Name, &protocol, &architecture, &isDefault, &item.ProviderKind); err != nil {
			return nil, "", err
		}
		if policy.Capabilities.SupportsArchitecture(architecture) && slices.Contains(policy.Protocols, protocol) {
			choices = append(choices, item)
			if isDefault == 1 {
				defaultID = item.ID
			}
		}
	}
	return choices, defaultID, rows.Err()
}

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
	resource, err := loadTemplateResource(ctx, tx, r.PathValue("templateId"))
	return AuthorizationTarget{Action: "template.create_revision", Resource: resource}, err
}

func loadTemplateResource(ctx context.Context, tx *sql.Tx, id string) (auth.Resource, error) {
	if !ValidUUID(id) {
		return auth.Resource{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid template identifier"}
	}
	resource := auth.Resource{Type: "template", ID: id}
	err := tx.QueryRowContext(ctx, "SELECT owner_user_id,state FROM project_templates WHERE id=$1", id).Scan(&resource.OwnerID, &resource.State)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Resource{}, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
	}
	return resource, err
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
		return templateResponse(view, http.StatusCreated), nil
	}
}

func templateResponse(view template.View, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(view)
	})
}

func listTemplates(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	query := r.URL.Query()
	cursor, archived := query.Get("cursor"), query.Get("archived")
	search := strings.TrimSpace(query.Get("q"))
	if cursor != "" && !ValidUUID(cursor) || archived != "" && archived != "true" && archived != "false" || len(search) > 200 {
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
	items, next, err := template.List(ctx, tx, p.User.ID, admin, state, cursor, search)
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
