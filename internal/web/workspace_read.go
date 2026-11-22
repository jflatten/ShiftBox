package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/launchlog"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

type workspaceView struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	OwnerID            string             `json:"ownerId"`
	TemplateID         string             `json:"templateId"`
	TemplateRevision   int64              `json:"templateRevision"`
	PlatformRelease    string             `json:"platformRelease"`
	ProviderKind       string             `json:"providerKind"`
	DesiredState       string             `json:"desiredState"`
	ObservedState      string             `json:"observedState"`
	Generation         int64              `json:"generation"`
	Resources          workspaceResources `json:"resources"`
	CurrentOperationID *string            `json:"currentOperationId"`
	Lifecycle          workspaceLifecycle `json:"lifecycle"`
	PreviewPorts       []int              `json:"previewPorts"`
	LastActivityAt     *string            `json:"lastActivityAt"`
	RetentionDeadline  *string            `json:"retentionDeadline"`
	Version            int64              `json:"version"`
	keepAliveOverride  *int64
	createdAt          string
	hasProviderVM      bool
}

type workspaceListItem struct {
	workspaceView
	Services []string `json:"services"`
}

type workspaceLifecycle struct {
	Current             string  `json:"current"`
	Next                *string `json:"next"`
	Mode                string  `json:"mode"`
	WorkflowOperationID *string `json:"workflowOperationId"`
}

type workspaceResources struct {
	CPUCount  int64 `json:"cpuCount"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskMiB   int64 `json:"diskMiB"`
}

const workspaceProjection = `w.id,w.owner_user_id,w.template_id,w.template_revision,a.release_id,a.provider_kind,
	w.desired_state,w.observed_state,w.generation,w.cpu_count,w.memory_mib,w.disk_mib,
	w.active_operation_id,w.last_activity_at,w.retention_deadline,w.version,w.preview_ports_json,
	COALESCE(NULLIF(w.name,''),w.id),w.keep_alive_minutes,w.created_at,
	w.lifecycle_current,w.lifecycle_next,w.lifecycle_mode,w.workflow_operation_id,(w.provider_instance_id IS NOT NULL)`

const workspaceFrom = ` FROM workspace_lifecycle w JOIN platform_artifacts a ON a.id=w.platform_artifact_id`

// List and detail read the same evidence in one SQL statement. Scheduled
// maintenance waits outside the journal until admitted for this workspace.
var workspaceLifecycleQuery = `
,lifecycle_steps(step_key,milestone,confirms) AS (VALUES ` + lifecycleStepValues() + `
), workflow AS (
	SELECT w.*,
		(SELECT o.id FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		 AND o.status IN ('pending','running','waiting') ORDER BY o.created_at DESC,o.id DESC LIMIT 1) AS active_operation_id,
		(SELECT o.id FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		 AND (o.type IN ('provision','start','rebuild','repair','stop','delete')
			OR (o.type='update' AND o.originating_action='maintenance.update_guest'))
		 ORDER BY o.created_at DESC,o.id DESC LIMIT 1) AS workflow_operation_id,
		CASE w.observed_state WHEN 'running' THEN 'vm-running' WHEN 'stopped' THEN 'stopped'
			WHEN 'deleted' THEN 'deleted' WHEN 'unreachable' THEN 'unreachable' ELSE 'unknown' END AS observed_milestone
	FROM workspace_candidates w
), evidence AS (
	SELECT w.*,o.type AS workflow_kind,o.status AS workflow_status,
		(SELECT m.milestone FROM operation_steps s JOIN lifecycle_steps m ON m.step_key=s.step_key
		 WHERE s.operation_id=o.id AND s.status='succeeded' AND m.confirms
		 ORDER BY s.step_index DESC LIMIT 1) AS verified,
		(SELECT m.milestone FROM operations prior
		 JOIN operation_steps s ON s.operation_id=prior.id JOIN lifecycle_steps m ON m.step_key=s.step_key
		 WHERE prior.workspace_id=w.id AND prior.generation=w.generation
		   AND prior.type IN ('provision','start','rebuild','repair','stop','delete')
		   AND (prior.created_at<o.created_at OR (prior.created_at=o.created_at AND prior.id<o.id))
		   AND s.status='succeeded' AND m.confirms
		 ORDER BY prior.created_at DESC,prior.id DESC,s.step_index DESC LIMIT 1) AS prior_verified,
		(SELECT m.milestone FROM operation_steps s JOIN lifecycle_steps m ON m.step_key=s.step_key
		 WHERE s.operation_id=o.id AND s.status IN ('pending','active') AND o.status IN ('pending','running','waiting')
		   AND NOT (o.type='delete' AND w.observed_state='stopped' AND m.milestone='stopped')
		 ORDER BY s.step_index LIMIT 1) AS target,
		EXISTS (SELECT 1 FROM operation_steps s JOIN lifecycle_steps m ON m.step_key=s.step_key
		WHERE s.operation_id=o.id AND o.status<>'cancelled' AND s.status='failed') AS transition_failed,
		EXISTS (SELECT 1 FROM operation_steps s JOIN lifecycle_steps m ON m.step_key=s.step_key
		WHERE s.operation_id=o.id AND s.transition_timeout_at IS NOT NULL AND o.status IN ('pending','running','waiting')
		   AND NOT EXISTS (
			 SELECT 1 FROM operation_steps confirmed JOIN lifecycle_steps verified ON verified.step_key=confirmed.step_key
			 WHERE confirmed.operation_id=o.id AND confirmed.status='succeeded' AND confirmed.step_index>=s.step_index
			   AND verified.confirms AND verified.milestone=m.milestone)) AS transition_timed_out,
		EXISTS (SELECT 1 FROM operation_steps s WHERE s.operation_id=o.id AND s.step_key='maintenance_boot') AS maintenance_boot
	FROM workflow w LEFT JOIN operations o ON o.id=w.workflow_operation_id
), workspace_lifecycle AS (
	SELECT w.*,
		CASE
		 WHEN observed_state='deleted' THEN 'deleted'
		 WHEN workflow_status IS NULL OR workflow_status='cancelled' THEN observed_milestone
		 WHEN workflow_status='succeeded' AND observed_state<>'running' THEN observed_milestone
		 WHEN workflow_kind='update' AND verified='updated' AND target IS NULL AND NOT transition_failed
			THEN COALESCE(prior_verified,observed_milestone)
		 ELSE COALESCE(verified,CASE
			WHEN workflow_kind='provision' THEN 'unknown'
			WHEN workflow_kind='start' OR maintenance_boot THEN 'stopped'
			WHEN observed_milestone IN ('stopped','unreachable') THEN observed_milestone
			WHEN observed_milestone='vm-running' AND prior_verified IN ('stopped','deleted','provisioned') THEN observed_milestone
			ELSE COALESCE(prior_verified,observed_milestone) END)
		END AS lifecycle_current,
		CASE WHEN observed_state='deleted' THEN NULL WHEN transition_failed THEN 'failed' ELSE target END AS lifecycle_next,
		CASE WHEN observed_state='deleted' THEN 'settled' WHEN transition_failed THEN 'failed'
			WHEN target IS NOT NULL AND transition_timed_out THEN 'timed_out'
			WHEN target IS NOT NULL THEN 'transitioning' ELSE 'settled' END AS lifecycle_mode
	FROM evidence w
)
SELECT ` + workspaceProjection + workspaceFrom

var workspaceDetailQuery = `WITH workspace_candidates AS (SELECT * FROM workspaces WHERE id=$1)` + workspaceLifecycleQuery

func lifecycleStepValues() string {
	steps := workspace.LifecycleSteps()
	values := make([]string, 0, len(steps))
	// The mapping contains only compile-time workspace constants.
	for _, step := range steps {
		values = append(values, fmt.Sprintf("('%s','%s',%t)", step.Key, step.Milestone, step.Confirms))
	}
	return strings.Join(values, ",")
}

// WorkspaceRetentionStatus reports whether the automatic cleanup scheduler can
// commit new deletions. The workspace's original deadline remains independent.
type WorkspaceRetentionStatus struct {
	State      string     `json:"state"`
	GraceUntil *time.Time `json:"graceUntil,omitempty"`
}

// WorkspaceReadHandlers returns the list and detail operations for management.
func WorkspaceReadHandlers(db *sql.DB, origin string, now func() time.Time,
	retention func(time.Time) WorkspaceRetentionStatus,
) (AuthorizedHandler, AuthorizedHandler) {
	store := auth.NewStore(db)
	list := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/workspaces", OperationID: "listWorkspaces", Action: "workspace.list"},
		Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "workspace.list", Resource: auth.Resource{Type: "workspace", OwnerID: principal.User.ID}}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return listWorkspaces(ctx, tx, r, store, principal, now())
		},
	}
	detail := AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodGet, Path: "/workspaces/{workspaceId}", OperationID: "getWorkspace", Action: "workspace.view"},
		Resolve: resolveWorkspaceView,
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			view, err := scanWorkspace(tx.QueryRowContext(ctx, workspaceDetailQuery, target.Resource.ID))
			if err != nil {
				return nil, err
			}
			keepAlivePolicy, err := workspace.ReadKeepAlivePolicy(ctx, tx)
			if err != nil {
				return nil, err
			}
			keepAliveMinutes := keepAlivePolicy.EffectiveMinutes(view.keepAliveOverride)
			var shutdownAt *string
			var inactivityMinutes int64
			if view.DesiredState == "running" {
				baseline := view.createdAt
				if view.LastActivityAt != nil {
					baseline = *view.LastActivityAt
				}
				last, err := time.Parse(time.RFC3339Nano, baseline)
				if err != nil {
					return nil, err
				}
				inactivityMinutes = max(0, int64(now().Sub(last)/time.Minute))
				if keepAliveMinutes > 0 {
					deadline := last.Add(time.Duration(keepAliveMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
					shutdownAt = &deadline
				}
			}
			resource, _, err := serviceWorkspace(ctx, tx, view.ID, "ide")
			if err != nil {
				return nil, err
			}
			entry, err := serviceEntryTarget(ctx, tx, store, principal, resource)
			if err != nil {
				return nil, err
			}
			repairResource, _, err := serviceWorkspace(ctx, tx, view.ID, "repair")
			if err != nil {
				return nil, err
			}
			repairEntry, err := serviceEntryTarget(ctx, tx, store, principal, repairResource)
			if err != nil {
				return nil, err
			}
			maintenance, err := workspace.ReadWorkspaceMaintenance(ctx, tx, view.ID, view.Generation)
			if err != nil {
				return nil, err
			}
			probes := []workspace.ProbeResult{}
			if view.DesiredState == "running" && view.ObservedState == "running" {
				probes, err = workspace.ReadProbeResults(ctx, tx, view.ID, view.Generation, now())
				if err != nil {
					return nil, err
				}
			}
			var lastFailedID *string
			var canStartRepair, canRetryAfterRepair bool
			var canInterruptDeployment bool
			if view.CurrentOperationID != nil && auth.Authorize(principal, "workspace.stop", target.Resource, nil, now()).Allowed {
				deployment, err := workspace.InterruptibleDeployment(ctx, tx, view.ID)
				if err != nil {
					return nil, err
				}
				canInterruptDeployment = deployment == *view.CurrentOperationID
			}
			if view.CurrentOperationID == nil {
				// A later successful workflow supersedes an older failure.
				err := tx.QueryRowContext(ctx, `SELECT CASE WHEN status='failed' THEN id END
					FROM operations WHERE workspace_id=$1 AND generation=$2
					ORDER BY created_at DESC,id DESC LIMIT 1`, view.ID, view.Generation).Scan(&lastFailedID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				if view.DesiredState == "running" && view.ObservedState == "running" &&
					auth.Authorize(principal, "workspace.repair", target.Resource, nil, now()).Allowed {
					canRetryAfterRepair, err = workspace.RepairActive(ctx, tx, view.ID, view.Generation)
					if err != nil {
						return nil, err
					}
					if lastFailedID != nil && !canRetryAfterRepair {
						canStartRepair, err = workspace.RepairAvailable(ctx, tx, view.ID, view.Generation, now())
						if err != nil {
							return nil, err
						}
					}
				}
			}
			freshness, err := imagecache.ReadWorkspaceFreshness(ctx, tx, view.ID, view.Generation)
			if err != nil {
				return nil, err
			}
			launchOperationID, err := launchlog.WorkspaceOperation(ctx, tx, view.ID)
			if err != nil {
				return nil, err
			}
			// Expose the current entry decision without issuing a grant.
			response := struct {
				workspaceView
				ImageCache               *imagecache.WorkspaceFreshness  `json:"imageCache,omitempty"`
				Probes                   []workspace.ProbeResult         `json:"probes"`
				CanOpenIDE               bool                            `json:"canOpenIde"`
				CanOpenRepair            bool                            `json:"canOpenRepair"`
				CanOpenPreviews          bool                            `json:"canOpenPreviews"`
				CanEditPreviewPorts      bool                            `json:"canEditPreviewPorts"`
				CanEditKeepAlive         bool                            `json:"canEditKeepAlive"`
				KeepAlivePolicy          workspace.KeepAlivePolicy       `json:"keepAlivePolicy"`
				KeepAliveMinutes         int64                           `json:"keepAliveMinutes"`
				KeepAliveOverrideMinutes *int64                          `json:"keepAliveOverrideMinutes"`
				InactivityShutdownAt     *string                         `json:"inactivityShutdownAt"`
				InactivityMinutes        int64                           `json:"inactivityMinutes"`
				CanStartRepair           bool                            `json:"canStartRepair"`
				CanRetryAfterRepair      bool                            `json:"canRetryAfterRepair"`
				CanInterruptDeployment   bool                            `json:"canInterruptDeployment"`
				CanStart                 bool                            `json:"canStart"`
				LaunchOperationID        *string                         `json:"launchOperationId,omitempty"`
				LastFailedOperationID    *string                         `json:"lastFailedOperationId"`
				Retention                *WorkspaceRetentionStatus       `json:"retention,omitempty"`
				Maintenance              *workspace.WorkspaceMaintenance `json:"maintenance,omitempty"`
			}{
				workspaceView: view,
				ImageCache:    freshness,
				Probes:        probes,
				CanOpenIDE:    auth.Authorize(principal, entry.Action, entry.Resource, entry.Grants, now()).Allowed,
				CanOpenRepair: auth.Authorize(principal, repairEntry.Action, repairEntry.Resource, repairEntry.Grants, now()).Allowed,
				CanOpenPreviews: view.DesiredState == "running" && view.ObservedState == "running" && auth.Authorize(principal, entry.Action,
					auth.Resource{Type: "workspace", ID: view.ID, OwnerID: view.OwnerID, Eligible: true}, entry.Grants, now()).Allowed,
				CanEditPreviewPorts:      auth.Authorize(principal, "workspace.update_policy", target.Resource, nil, now()).Allowed,
				CanEditKeepAlive:         auth.Authorize(principal, "workspace.update_policy", target.Resource, nil, now()).Allowed,
				KeepAlivePolicy:          keepAlivePolicy,
				KeepAliveMinutes:         keepAliveMinutes,
				KeepAliveOverrideMinutes: view.keepAliveOverride,
				InactivityShutdownAt:     shutdownAt,
				InactivityMinutes:        inactivityMinutes,
				CanStartRepair:           canStartRepair,
				CanRetryAfterRepair:      canRetryAfterRepair,
				CanInterruptDeployment:   canInterruptDeployment,
				CanStart:                 view.hasProviderVM && auth.Authorize(principal, "workspace.start", target.Resource, nil, now()).Allowed,
				LaunchOperationID:        launchOperationID,
				LastFailedOperationID:    lastFailedID,
				Maintenance:              maintenance,
			}
			if retention != nil && view.RetentionDeadline != nil {
				status := retention(now())
				response.Retention = &status
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(view.Version, 10)))
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}), nil
		},
	}
	return list, detail
}

func resolveWorkspaceView(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
	id := r.PathValue("workspaceId")
	if !ValidUUID(id) {
		return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_id", Title: "Invalid workspace identifier"}
	}
	resource := auth.Resource{Type: "workspace", ID: id}
	err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM workspaces WHERE id=$1`, id).Scan(&resource.OwnerID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && resource.OwnerID != principal.User.ID && principal.User.Role != "administrator") {
		return AuthorizationTarget{}, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	return AuthorizationTarget{Action: "workspace.view", Resource: resource}, nil
}

func listWorkspaces(ctx context.Context, tx *sql.Tx, r *http.Request, store *auth.Store, principal auth.Principal, now time.Time) (http.Handler, error) {
	query := r.URL.Query()
	owner, state, cursor := query.Get("owner"), query.Get("state"), query.Get("cursor")
	excludeDeleted, lifecycleState := query.Get("excludeDeleted"), query.Get("lifecycleState")
	states := []string{"unknown", "provisioning", "stopped", "starting", "running", "unreachable", "stopping", "deleting", "deleted", "failed"}
	milestones := []string{"unknown", "provisioned", "stopped", "vm-running", "container-ready", "ready", "repair-ready", "updated", "unreachable", "deleted"}
	if (owner != "" && !ValidUUID(owner)) || (cursor != "" && !ValidUUID(cursor)) ||
		(state != "" && !slices.Contains(states, state)) ||
		(lifecycleState != "" && !slices.Contains(milestones, lifecycleState)) ||
		(excludeDeleted != "" && excludeDeleted != "true" && excludeDeleted != "false") {
		return nil, Problem{Status: 400, Code: "request.invalid_filter", Title: "Check the workspace filters"}
	}
	const limit = 50
	admin := principal.User.Role == "administrator"
	rows, err := tx.QueryContext(ctx, `WITH workspace_candidates AS (SELECT w.* FROM workspaces w
		WHERE ($1 OR w.owner_user_id=$2) AND ($3='' OR w.owner_user_id=$3)
		  AND ($4='' OR w.observed_state=$4) AND w.id>$5
		  AND (NOT $6 OR w.observed_state<>'deleted'))`+workspaceLifecycleQuery+`
		WHERE ($7='' OR w.lifecycle_current=$7) ORDER BY w.id LIMIT $8`,
		admin, principal.User.ID, owner, state, cursor, excludeDeleted == "true", lifecycleState, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := struct {
		Items      []workspaceListItem `json:"items"`
		NextCursor *string             `json:"nextCursor"`
	}{Items: make([]workspaceListItem, 0, limit)}
	for rows.Next() {
		view, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		if len(result.Items) == limit {
			result.NextCursor = &result.Items[limit-1].ID
			break
		}
		result.Items = append(result.Items, workspaceListItem{workspaceView: view, Services: []string{}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Release the query before reading service availability on the same transaction.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range result.Items {
		item := &result.Items[i]
		available, err := codeServerAvailable(ctx, tx, store, principal, item.workspaceView, now)
		if err != nil {
			return nil, err
		}
		if available {
			item.Services = append(item.Services, workspace.ProbeCodeServer)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}), nil
}

// codeServerAvailable applies the existing IDE entry rules to a fresh healthy probe.
func codeServerAvailable(ctx context.Context, tx *sql.Tx, store *auth.Store, principal auth.Principal, view workspaceView, now time.Time) (bool, error) {
	if view.DesiredState != "running" || view.ObservedState != "running" {
		return false, nil
	}
	probes, err := workspace.ReadProbeResults(ctx, tx, view.ID, view.Generation, now)
	if err != nil {
		return false, err
	}
	if !slices.Contains(probes, workspace.ProbeResult{ID: workspace.ProbeCodeServer, Status: workspace.ProbeHealthy}) {
		return false, nil
	}
	resource, generation, err := serviceWorkspace(ctx, tx, view.ID, "ide")
	if err != nil {
		return false, err
	}
	if !resource.Eligible || generation != view.Generation {
		return false, nil
	}
	entry, err := serviceEntryTarget(ctx, tx, store, principal, resource)
	if err != nil {
		return false, err
	}
	return auth.Authorize(principal, entry.Action, entry.Resource, entry.Grants, now).Allowed, nil
}

func scanWorkspace(row interface{ Scan(...any) error }) (workspaceView, error) {
	var view workspaceView
	var previewJSON string
	err := row.Scan(&view.ID, &view.OwnerID, &view.TemplateID, &view.TemplateRevision, &view.PlatformRelease, &view.ProviderKind,
		&view.DesiredState, &view.ObservedState, &view.Generation,
		&view.Resources.CPUCount, &view.Resources.MemoryMiB, &view.Resources.DiskMiB,
		&view.CurrentOperationID, &view.LastActivityAt, &view.RetentionDeadline, &view.Version, &previewJSON, &view.Name,
		&view.keepAliveOverride, &view.createdAt, &view.Lifecycle.Current, &view.Lifecycle.Next, &view.Lifecycle.Mode,
		&view.Lifecycle.WorkflowOperationID, &view.hasProviderVM)
	if err != nil {
		return view, err
	}
	view.PreviewPorts, err = workspace.ParsePreviewPorts(previewJSON)
	return view, err
}
