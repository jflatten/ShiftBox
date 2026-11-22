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
	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type profileInput struct {
	workspaceResources
	Name               string `json:"name"`
	InstanceType       string `json:"instanceType,omitempty"`
	PlatformArtifactID string `json:"platformArtifactId,omitempty"`
	AMIFamily          string `json:"amiFamily,omitempty"`
}

type infrastructureProvider struct {
	Name           string                  `json:"name"`
	Version        string                  `json:"version"`
	Architecture   string                  `json:"architecture"`
	Architectures  []string                `json:"architectures,omitempty"`
	NetworkModes   []string                `json:"networkModes"`
	InstanceTypes  []provider.InstanceType `json:"instanceTypes,omitempty"`
	MinimumDiskGiB int64                   `json:"minimumDiskGiB,omitempty"`
	MaximumDiskGiB int64                   `json:"maximumDiskGiB,omitempty"`
	OfficialImage  *provider.OfficialAMI   `json:"officialImage,omitempty"`
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
		Name:          caps.Name,
		Version:       caps.Version,
		Architecture:  caps.Architecture,
		Architectures: slices.Clone(caps.Architectures),
		NetworkModes:  slices.Clone(caps.NetworkModes),
	}
	if caps.Name == "ec2" {
		providerView.InstanceTypes = slices.Clone(caps.InstanceTypes)
		providerView.MinimumDiskGiB = caps.Minimum.DiskBytes >> 30
		providerView.MaximumDiskGiB = caps.Maximum.DiskBytes >> 30
		providerView.OfficialImage = caps.OfficialImage
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
			keepAlivePolicy, err := workspace.ReadKeepAlivePolicy(ctx, tx)
			if err != nil {
				return nil, err
			}
			retention, err := launchlog.ReadRetention(ctx, tx)
			if err != nil {
				return nil, err
			}
			conditions, err := browserCertificateConditions(ctx, tx, now())
			if err != nil {
				return nil, err
			}
			rows, err := tx.QueryContext(ctx, `SELECT condition_key,severity,detail FROM operational_conditions ORDER BY condition_key`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				condition := infrastructureCondition{Active: true}
				if err := rows.Scan(&condition.Key, &condition.Severity, &condition.Detail); err != nil {
					return nil, err
				}
				conditions = append(conditions, condition)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(version, 10)))
				_ = json.NewEncoder(w).Encode(struct {
					Provider           infrastructureProvider    `json:"provider"`
					Capacity           workspaceResources        `json:"capacity"`
					Conditions         []infrastructureCondition `json:"conditions"`
					Version            int64                     `json:"version"`
					KeepAlivePolicy    workspace.KeepAlivePolicy `json:"keepAlivePolicy"`
					LaunchLogRetention launchlog.Retention       `json:"launchLogRetention"`
				}{Provider: providerView, Capacity: capacity, Conditions: conditions, Version: version, KeepAlivePolicy: keepAlivePolicy, LaunchLogRetention: retention})
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
func CapacityHandlers(db *sql.DB, origin string, now func() time.Time, newID func() string, policy WorkspaceLaunchPolicy) (AuthorizedHandler, AuthorizedHandler) {
	caps := policy.Capabilities
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
			if err := input.normalize(caps); err != nil {
				return nil, err
			}
			imageID, err := input.resolveAMI(ctx, tx, policy)
			if err != nil {
				return nil, err
			}
			if newID == nil {
				return nil, errors.New("resource profile identity generator is missing")
			}
			err = workspace.CreateResourceProfileInTx(ctx, tx, workspace.ResourceProfile{
				ID: newID(), Name: input.Name, Allocation: input.profileAllocation(), Enabled: true, PlatformArtifactID: imageID, AMIFamily: input.AMIFamily,
			}, caps)
			switch {
			case errors.Is(err, provider.ErrCapabilityUnsupported):
				return nil, resourceProfileUnsupported()
			case errors.Is(err, workspace.ErrCapacityConflict):
				return nil, resourceProfileNameConflict()
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

func (input *profileInput) normalize(caps provider.Capabilities) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 || input.DiskMiB < 1 {
		return Problem{Status: 400, Code: "infrastructure.invalid_resource_profile", Title: "Enter a profile name and positive resources"}
	}
	if caps.Name == "ec2" {
		input.InstanceType = input.profileAllocation().EC2Type()
		instance, ok := caps.LookupInstanceType(input.InstanceType)
		if !ok {
			return Problem{Status: 409, Code: "infrastructure.resource_profile_unsupported", Title: "Choose an offered EC2 instance type"}
		}
		// AWS supplies compute facts. A profile may only configure its EBS size.
		input.CPUCount = int64(instance.CPUCount)
		input.MemoryMiB = instance.MemoryMiB
	} else if !input.positive() {
		return Problem{Status: 400, Code: "infrastructure.invalid_resource_profile", Title: "Enter positive CPU and memory values"}
	}
	return nil
}

func (input profileInput) profileAllocation() workspace.Allocation {
	allocation := input.allocation()
	allocation.InstanceType = input.InstanceType
	return allocation
}

func resourceProfileUnsupported() Problem {
	return Problem{Status: 409, Code: "infrastructure.resource_profile_unsupported", Title: "Choose resources within the provider limits"}
}

func resourceProfileNameConflict() Problem {
	return Problem{Status: 409, Code: "infrastructure.resource_profile_conflict", Title: "Choose a different resource profile name"}
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
	// Zero removes enforcement for a budget dimension; workspace allocations
	// and resource profiles still require positive resources.
	if input.CPUCount < 0 || input.MemoryMiB < 0 || input.DiskMiB < 0 || int64(int(input.CPUCount)) != input.CPUCount {
		return nil, Problem{Status: 400, Code: "infrastructure.invalid_capacity", Title: "Enter nonnegative whole capacity limits; zero means Unlimited"}
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
