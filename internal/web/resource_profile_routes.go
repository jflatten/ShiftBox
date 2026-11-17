package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type resourceProfileView struct {
	profileInput
	ID                string `json:"id"`
	Enabled           bool   `json:"enabled"`
	Version           int64  `json:"version"`
	PlatformImageName string `json:"platformImageName,omitempty"`
}

// ResourceProfileHandlers returns the read, update, and delete routes for the administrator catalog.
func ResourceProfileHandlers(db *sql.DB, origin string, now func() time.Time, policy WorkspaceLaunchPolicy) []AuthorizedHandler {
	caps := policy.Capabilities
	const collection = "/admin/resource-profiles"
	const item = collection + "/{profileId}"
	list := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: collection, OperationID: "listResourceProfiles", Action: "infrastructure.view"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "infrastructure.view"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.cpu_count,p.memory_mib,p.disk_mib,p.enabled,p.version,p.instance_type,
				COALESCE(p.platform_artifact_id,''),COALESCE(a.release_id,'')
				FROM resource_profiles p LEFT JOIN platform_artifacts a ON a.id=p.platform_artifact_id ORDER BY p.name,p.id`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			profiles := []resourceProfileView{}
			for rows.Next() {
				var profile resourceProfileView
				if err := rows.Scan(&profile.ID, &profile.Name, &profile.CPUCount, &profile.MemoryMiB, &profile.DiskMiB,
					&profile.Enabled, &profile.Version, &profile.InstanceType, &profile.PlatformArtifactID, &profile.PlatformImageName); err != nil {
					return nil, err
				}
				if caps.Name == "ec2" {
					profile.InstanceType = profile.profileAllocation().EC2Type()
				}
				profiles = append(profiles, profile)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(profiles)
			}), nil
		},
	}
	update := resourceProfileMutation(db, origin, now,
		ManagementRoute{Method: http.MethodPut, Path: item, OperationID: "updateResourceProfile", Action: "infrastructure.manage"},
		func(ctx context.Context, tx *sql.Tx, r *http.Request, id string) error {
			var input struct {
				profileInput
				Enabled *bool `json:"enabled"`
			}
			if err := DecodeJSON(nil, r, &input, 4096); err != nil {
				return err
			}
			if err := input.normalize(caps); err != nil {
				return err
			}
			if err := input.validateAMI(ctx, tx, policy); err != nil {
				return err
			}
			if input.Enabled == nil {
				return Problem{Status: 400, Code: "infrastructure.invalid_resource_profile", Title: "Choose whether the profile is enabled"}
			}
			if _, err := workspace.ProfileResources(input.profileAllocation(), caps); err != nil {
				return resourceProfileUnsupported()
			}
			enabled := 0
			if *input.Enabled {
				enabled = 1
			}
			result, err := tx.ExecContext(ctx, `UPDATE resource_profiles SET name=$2,cpu_count=$3,memory_mib=$4,disk_mib=$5,
				enabled=$6,instance_type=$7,platform_artifact_id=NULLIF($8,''),version=version+1 WHERE id=$1
				AND NOT EXISTS (SELECT 1 FROM resource_profiles WHERE name=$2 AND id<>$1)`,
				id, input.Name, input.CPUCount, input.MemoryMiB, input.DiskMiB, enabled, input.InstanceType, input.PlatformArtifactID)
			return checkResourceProfileWrite(result, err, resourceProfileNameConflict())
		})
	remove := resourceProfileMutation(db, origin, now,
		ManagementRoute{Method: http.MethodDelete, Path: item, OperationID: "deleteResourceProfile", Action: "infrastructure.manage"},
		func(ctx context.Context, tx *sql.Tx, _ *http.Request, id string) error {
			// Preserve every revision's reference, including historical revisions.
			result, err := tx.ExecContext(ctx, `DELETE FROM resource_profiles WHERE id=$1
				AND NOT EXISTS (SELECT 1 FROM template_revisions WHERE resource_profile_id=$1)`, id)
			return checkResourceProfileWrite(result, err, Problem{Status: 409, Code: "infrastructure.resource_profile_in_use",
				Title: "A template revision uses this resource profile", Detail: "You can disable the profile to prevent future workspace launches that use it."})
		})
	return []AuthorizedHandler{list, update, remove}
}

func resourceProfileMutation(db *sql.DB, origin string, now func() time.Time, route ManagementRoute,
	apply func(context.Context, *sql.Tx, *http.Request, string) error) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now, Route: route,
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: route.Action}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			expected, err := RequireVersion(r)
			if err != nil {
				return nil, err
			}
			id := r.PathValue("profileId")
			// The installation database serializes controller transactions, so the
			// version check and mutation observe the same catalog state.
			var version int64
			err = tx.QueryRowContext(ctx, "SELECT version FROM resource_profiles WHERE id=$1", id).Scan(&version)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, Problem{Status: 404, Code: "infrastructure.resource_profile_not_found", Title: "This resource profile no longer exists"}
			case err != nil:
				return nil, err
			case version != expected:
				return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "This resource profile changed. Reload the profiles and try again."}
			}
			if err := apply(ctx, tx, r, id); err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
		},
	}
}

func checkResourceProfileWrite(result sql.Result, err error, conflict Problem) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return conflict
	}
	return nil
}
