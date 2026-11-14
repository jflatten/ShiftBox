package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type profileInput struct {
	workspaceResources
	Name string `json:"name"`
}

// CapacityHandlers returns the reviewed administrator budget and profile routes.
// Provider bounds come from the controller's validated provider configuration.
func CapacityHandlers(db *sql.DB, origin string, now func() time.Time, newID func() string, caps provider.Capabilities) (AuthorizedHandler, AuthorizedHandler) {
	resolve := func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
		return AuthorizationTarget{Action: "infrastructure.manage"}, nil
	}
	budget := AuthorizedHandler{
		DB: db, Origin: origin, Now: now, Resolve: resolve,
		Route: ManagementRoute{Method: http.MethodPut, Path: "/admin/capacity", OperationID: "updateCapacityBudget", Action: "infrastructure.manage"},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var id string
			if err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&id); err != nil {
				return nil, err
			}
			return ApplyCapacityBudget(ctx, tx, r, id, actor.User.ID, now())
		},
	}
	profile := AuthorizedHandler{
		DB: db, Origin: origin, Now: now, Resolve: resolve,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/admin/resource-profiles", OperationID: "createResourceProfile", Action: "infrastructure.manage"},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var input profileInput
			if err := DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			input.Name = strings.TrimSpace(input.Name)
			if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 || !input.positive() {
				return nil, Problem{Status: 400, Code: "infrastructure.invalid_resource_profile", Title: "Enter a profile name and positive resources"}
			}
			if newID == nil {
				return nil, errors.New("resource profile identity generator is missing")
			}
			err := workspace.CreateResourceProfileInTx(ctx, tx, workspace.ResourceProfile{
				ID: newID(), Name: input.Name, Allocation: input.allocation(), Enabled: true,
			}, caps)
			switch {
			case errors.Is(err, provider.ErrCapabilityUnsupported):
				return nil, Problem{Status: 409, Code: "infrastructure.resource_profile_unsupported", Title: "Choose resources within the provider limits"}
			case errors.Is(err, workspace.ErrCapacityConflict):
				return nil, Problem{Status: 409, Code: "infrastructure.resource_profile_conflict", Title: "Choose a different resource profile name"}
			case err != nil:
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(input)
			}), nil
		},
	}
	return budget, profile
}

// ApplyCapacityBudget handles the shared, versioned capacity write after the
// caller authorizes the request in the same transaction.
func ApplyCapacityBudget(ctx context.Context, tx *sql.Tx, r *http.Request, installation, actor string, now time.Time) (http.Handler, error) {
	version, err := RequireVersion(r)
	if err != nil {
		return nil, err
	}
	var input workspaceResources
	if err := DecodeJSON(nil, r, &input, 4096); err != nil {
		return nil, err
	}
	if !input.positive() {
		return nil, Problem{Status: 400, Code: "infrastructure.invalid_capacity", Title: "Enter positive capacity limits"}
	}
	err = workspace.SetCapacityBudgetInTx(ctx, tx, installation, input.allocation(), version, actor, now)
	if errors.Is(err, workspace.ErrCapacityConflict) {
		return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the capacity budget before updating"}
	}
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
}

func (r workspaceResources) positive() bool {
	return r.CPUCount > 0 && r.MemoryMiB > 0 && r.DiskMiB > 0 && int64(int(r.CPUCount)) == r.CPUCount
}

func (r workspaceResources) allocation() workspace.Allocation {
	return workspace.Allocation{CPU: int(r.CPUCount), MemoryMiB: r.MemoryMiB, DiskMiB: r.DiskMiB}
}
