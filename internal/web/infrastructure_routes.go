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
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type profileInput struct {
	workspaceResources
	Name string `json:"name"`
}

type infrastructureProvider struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Architecture string   `json:"architecture"`
	NetworkModes []string `json:"networkModes"`
}

type infrastructureCondition struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Active   bool   `json:"active"`
	Detail   string `json:"detail"`
}

// InfrastructureStatusHandler reports the pinned provider capabilities and
// current capacity budget through the administrator-only management route.
func InfrastructureStatusHandler(db *sql.DB, origin string, now func() time.Time, caps provider.Capabilities) AuthorizedHandler {
	providerView := infrastructureProvider{
		Name:         caps.Name,
		Version:      caps.Version,
		Architecture: caps.Architecture,
		NetworkModes: slices.Clone(caps.NetworkModes),
	}
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/admin/infrastructure", OperationID: "getInfrastructureStatus", Action: "infrastructure.view"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "infrastructure.view"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var capacity workspaceResources
			var version int64
			err := tx.QueryRowContext(ctx, `SELECT b.cpu_limit,b.memory_mib_limit,b.disk_mib_limit,b.version
				FROM installations i JOIN capacity_budgets b ON b.installation_id=i.id
				WHERE i.singleton=1`).Scan(&capacity.CPUCount, &capacity.MemoryMiB, &capacity.DiskMiB, &version)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, Problem{Status: 503, Code: "infrastructure.capacity_unavailable", Title: "The capacity budget is unavailable"}
			}
			if err != nil {
				return nil, err
			}
			conditions, err := browserCertificateConditions(ctx, tx, now())
			if err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(version, 10)))
				_ = json.NewEncoder(w).Encode(struct {
					Provider   infrastructureProvider    `json:"provider"`
					Capacity   workspaceResources        `json:"capacity"`
					Conditions []infrastructureCondition `json:"conditions"`
					Version    int64                     `json:"version"`
				}{Provider: providerView, Capacity: capacity, Conditions: conditions, Version: version})
			}), nil
		},
	}
}

// Derive the warning from recorded validity dates so it survives restarts
// and clears when a healthy replacement becomes active.
func browserCertificateConditions(ctx context.Context, tx *sql.Tx, now time.Time) ([]infrastructureCondition, error) {
	var start, end string
	err := tx.QueryRowContext(ctx, "SELECT not_before,not_after FROM browser_certificates WHERE state='active'").Scan(&start, &end)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	notBefore, startErr := time.Parse(time.RFC3339Nano, start)
	notAfter, endErr := time.Parse(time.RFC3339Nano, end)
	switch {
	case startErr != nil || endErr != nil || now.Before(notBefore) || !now.Before(notAfter):
		return []infrastructureCondition{{Key: "browser_certificate.invalid", Severity: "critical", Active: true,
			Detail: "Activate a valid browser certificate for the management, IDE, and preview origins."}}, nil
	case notAfter.Sub(now) <= 30*24*time.Hour:
		return []infrastructureCondition{{Key: "browser_certificate.expiring", Severity: "warning", Active: true,
			Detail: "Replace the browser certificate before " + notAfter.UTC().Format("2006-01-02 15:04 UTC") + "."}}, nil
	default:
		return []infrastructureCondition{}, nil
	}
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
