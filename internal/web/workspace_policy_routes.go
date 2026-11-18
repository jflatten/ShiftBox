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
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// WorkspacePolicyHandler updates policy without changing the template or launch snapshot.
func WorkspacePolicyHandler(db *sql.DB, origin string, now func() time.Time, policy WorkspaceLaunchPolicy, registry *proxy.Registry) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPatch, Path: "/workspaces/{workspaceId}", OperationID: "updateWorkspacePolicy", Action: "workspace.update_policy"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			target, err := resolveWorkspaceView(ctx, tx, r, principal)
			target.Action = "workspace.update_policy"
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return applyWorkspacePolicy(ctx, tx, r, target.Resource.ID, policy, now(), registry)
		},
	}
}

func applyWorkspacePolicy(ctx context.Context, tx *sql.Tx, r *http.Request, id string, policy WorkspaceLaunchPolicy, now time.Time, registry *proxy.Registry) (http.Handler, error) {
	version, err := RequireVersion(r)
	if err != nil {
		return nil, err
	}
	var input struct {
		RetentionSeconds json.RawMessage `json:"retentionSeconds"`
		NetworkPolicyID  json.RawMessage `json:"networkPolicyId"`
		PreviewPorts     json.RawMessage `json:"previewPorts"`
		KeepAliveMinutes json.RawMessage `json:"keepAliveMinutes"`
	}
	if err := DecodeJSON(nil, r, &input, 4096); err != nil {
		return nil, err
	}
	var retention *int64
	if input.RetentionSeconds != nil &&
		(json.Unmarshal(input.RetentionSeconds, &retention) != nil || (retention != nil && *retention < 1)) {
		return nil, invalidAuthorizationFields()
	}
	if retention != nil && (policy.RetentionMaximum == nil || *retention > *policy.RetentionMaximum || workspace.RetentionDeadline(now, retention) == nil) {
		return nil, Problem{Status: 409, Code: "workspace.retention_unavailable", Title: "Choose an allowed retention period"}
	}
	var network *string
	if input.NetworkPolicyID != nil {
		var policyID string
		if json.Unmarshal(input.NetworkPolicyID, &policyID) != nil || !ValidUUID(policyID) {
			return nil, invalidAuthorizationFields()
		}
		snapshot, err := workspace.LaunchNetworkSnapshot(ctx, tx, policyID, policy.Capabilities)
		if errors.Is(err, provider.ErrCapabilityUnsupported) {
			return nil, Problem{Status: 409, Code: "workspace.network_policy_unsupported", Title: "The provider cannot enforce this network policy"}
		}
		if err != nil {
			return nil, err
		}
		network = &snapshot
	}
	var ports []int
	var previewJSON *string
	if input.PreviewPorts != nil {
		ports, err = workspace.ParsePreviewPorts(string(input.PreviewPorts))
		if err != nil {
			return nil, invalidAuthorizationFields()
		}
		encoded, _ := json.Marshal(ports)
		value := string(encoded)
		previewJSON = &value
	}
	var keepAlive *int64
	if input.KeepAliveMinutes != nil && json.Unmarshal(input.KeepAliveMinutes, &keepAlive) != nil {
		return nil, invalidAuthorizationFields()
	}
	if input.RetentionSeconds == nil && network == nil && previewJSON == nil && input.KeepAliveMinutes == nil {
		return nil, invalidAuthorizationFields()
	}
	// Share the lifecycle admission lock so a policy decision cannot race the
	// start, stop, or retention operation it checks below.
	if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
		return nil, err
	}
	if keepAlive != nil {
		settings, err := workspace.ReadKeepAlivePolicy(ctx, tx)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(settings.AllowedMinutes, *keepAlive) ||
			(settings.MaximumMinutes > 0 && (*keepAlive == 0 || *keepAlive > settings.MaximumMinutes)) {
			return nil, Problem{Status: 409, Code: "workspace.keep_alive_unavailable", Title: "Choose one of the administrator's keep-alive values"}
		}
	}
	view, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceDetailQuery, id))
	if err != nil {
		return nil, err
	}
	if view.Version != version {
		return nil, workspacePolicyStale()
	}
	if view.CurrentOperationID != nil || view.DesiredState == "deleted" || view.ObservedState == "deleted" ||
		(network != nil && (view.DesiredState != "stopped" || view.ObservedState != "stopped")) {
		return nil, Problem{Status: 409, Code: "workspace.policy_unavailable", Title: "Wait for current work to finish and stop the workspace before changing its network policy"}
	}
	if network != nil {
		if err := workspace.VerifyStoppedNetwork(ctx, tx, id, view.Generation, *network, policy.VerifyNetwork); err != nil {
			if errors.Is(err, provider.ErrOutcomeUnknown) {
				return nil, Problem{Status: 409, Code: "workspace.network_policy_unverified", Title: "Verify the stopped workspace network in the provider before saving this policy"}
			}
			return nil, err
		}
	}
	var baseline string
	var deadline *string
	removed := map[string]bool{}
	if previewJSON != nil {
		var previous string
		if err := tx.QueryRowContext(ctx, "SELECT preview_ports_json FROM workspaces WHERE id=$1", id).Scan(&previous); err != nil {
			return nil, err
		}
		old, err := workspace.ParsePreviewPorts(previous)
		if err != nil {
			return nil, err
		}
		for _, port := range old {
			if slices.Contains(ports, port) {
				continue
			}
			service := "preview-" + strconv.Itoa(port)
			removed[service] = true
			if _, err := tx.ExecContext(ctx, "UPDATE origin_grants SET revoked_at=$1 WHERE workspace_id=$2 AND service_key=$3 AND revoked_at IS NULL", now.UTC().Format(time.RFC3339Nano), id, service); err != nil {
				return nil, err
			}
		}
	}
	if retention != nil {
		// A policy edit does not count as workspace activity. Stopped and
		// never-connected workspaces use their last known activity or launch.
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(last_activity_at,created_at) FROM workspaces WHERE id=$1", id).Scan(&baseline); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, baseline)
		if err != nil {
			return nil, err
		}
		deadline = workspace.RetentionDeadline(at, retention)
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspaces SET
		retention_seconds=CASE WHEN $1 THEN CAST($2 AS BIGINT) ELSE retention_seconds END,
		retention_deadline=CASE WHEN $1 THEN CAST($7 AS TEXT) ELSE retention_deadline END,
		network_policy_override_json=COALESCE(CAST($3 AS TEXT),network_policy_override_json),
		preview_ports_json=COALESCE(CAST($9 AS TEXT),preview_ports_json),
		keep_alive_minutes=CASE WHEN $10 THEN CAST($11 AS BIGINT) ELSE keep_alive_minutes END,
		updated_at=$4,version=version+1
		WHERE id=$5 AND version=$6 AND EXISTS (SELECT 1 FROM installations WHERE singleton=1 AND state='active')
		AND ($8='' OR COALESCE(last_activity_at,created_at)=$8)`,
		input.RetentionSeconds != nil, retention, network, now.UTC().Format(time.RFC3339Nano), id, version, deadline, baseline, previewJSON,
		input.KeepAliveMinutes != nil, keepAlive)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, workspacePolicyStale()
	}
	view, err = scanWorkspace(tx.QueryRowContext(ctx, workspaceDetailQuery, id))
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if registry != nil && len(removed) > 0 {
			if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.WorkspaceID == id && removed[scope.Service] }); err != nil {
				WriteProblem(w, RequestID(r.Context()), err)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
		_ = json.NewEncoder(w).Encode(view)
	}), nil
}

func workspacePolicyStale() Problem {
	return Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the workspace before changing its policy"}
}
