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
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// WorkspacePolicyHandler updates policy without changing the template or launch snapshot.
func WorkspacePolicyHandler(db *sql.DB, origin string, now func() time.Time, policy WorkspaceLaunchPolicy) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPatch, Path: "/workspaces/{workspaceId}", OperationID: "updateWorkspacePolicy", Action: "workspace.update_policy"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			target, err := resolveWorkspaceView(ctx, tx, r, principal)
			target.Action = "workspace.update_policy"
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return applyWorkspacePolicy(ctx, tx, r, target.Resource.ID, policy, now())
		},
	}
}

func applyWorkspacePolicy(ctx context.Context, tx *sql.Tx, r *http.Request, id string, policy WorkspaceLaunchPolicy, now time.Time) (http.Handler, error) {
	version, err := RequireVersion(r)
	if err != nil {
		return nil, err
	}
	var input struct {
		RetentionSeconds json.RawMessage `json:"retentionSeconds"`
		NetworkPolicyID  json.RawMessage `json:"networkPolicyId"`
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
	if input.RetentionSeconds == nil && network == nil {
		return nil, invalidAuthorizationFields()
	}
	// Share the lifecycle admission lock so a policy decision cannot race the
	// start, stop, or retention operation it checks below.
	if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
		return nil, err
	}
	view, err := scanWorkspace(tx.QueryRowContext(ctx, "SELECT "+workspaceProjection+workspaceFrom+" WHERE w.id=$1", id))
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
	var baseline string
	var deadline *string
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
		network_policy_override_json=COALESCE(CAST($3 AS TEXT),network_policy_override_json),updated_at=$4,version=version+1
		WHERE id=$5 AND version=$6 AND EXISTS (SELECT 1 FROM installations WHERE singleton=1 AND state='active')
		AND ($8='' OR COALESCE(last_activity_at,created_at)=$8)`,
		input.RetentionSeconds != nil, retention, network, now.UTC().Format(time.RFC3339Nano), id, version, deadline, baseline)
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
	view, err = scanWorkspace(tx.QueryRowContext(ctx, "SELECT "+workspaceProjection+workspaceFrom+" WHERE w.id=$1", id))
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
		_ = json.NewEncoder(w).Encode(view)
	}), nil
}

func workspacePolicyStale() Problem {
	return Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the workspace before changing its policy"}
}
