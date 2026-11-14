package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/template"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// WorkspaceLaunchPolicy holds current controller-side admission settings.
type WorkspaceLaunchPolicy struct {
	Capabilities     provider.Capabilities
	Protocols        []string
	RetentionMaximum *int64
}

type workspaceLaunchRequest struct {
	TemplateID       string `json:"templateId"`
	TemplateRevision int64  `json:"templateRevision"`
	RetentionSeconds *int64 `json:"retentionSeconds"`
	key              string
	revision         template.LaunchRevision
}

// WorkspaceLaunchHandler admits a new workspace through the management
// router. Only database work runs inside the authority transaction.
func WorkspaceLaunchHandler(db *sql.DB, origin string, now func() time.Time, journal *operation.Journal, policy WorkspaceLaunchPolicy) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route:   ManagementRoute{Method: http.MethodPost, Path: "/workspaces", OperationID: "launchWorkspace", Action: "workspace.launch"},
		Resolve: resolveWorkspaceLaunch,
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			if journal == nil || target.launch == nil {
				return nil, errors.New("workspace launch is incompletely wired")
			}
			op, err := applyWorkspaceLaunch(ctx, tx, journal, actor.User.ID, *target.launch, policy, now())
			if err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(op)
			}), nil
		},
	}
}

func resolveWorkspaceLaunch(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal) (AuthorizationTarget, error) {
	var input workspaceLaunchRequest
	if err := DecodeJSON(nil, r, &input, 4096); err != nil {
		return AuthorizationTarget{}, err
	}
	if !ValidUUID(input.TemplateID) || input.TemplateRevision < 1 || (input.RetentionSeconds != nil && *input.RetentionSeconds < 1) {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	key, err := IdempotencyKey(r)
	if err != nil {
		return AuthorizationTarget{}, err
	}
	input.key = key
	input.revision, err = template.ResolveLaunchRevision(ctx, tx, input.TemplateID, input.TemplateRevision, actor.User.ID)
	if errors.Is(err, sql.ErrNoRows) {
		// The same denial also applies when the caller lacks a launch grant.
		return AuthorizationTarget{}, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
	}
	if err != nil {
		return AuthorizationTarget{}, err
	}
	return AuthorizationTarget{Action: "workspace.launch", Resource: input.revision.Resource, launch: &input}, nil
}

func applyWorkspaceLaunch(ctx context.Context, tx *sql.Tx, journal *operation.Journal, actor string, input workspaceLaunchRequest, policy WorkspaceLaunchPolicy, now time.Time) (operation.Operation, error) {
	revision := input.revision
	var installation string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM installations WHERE singleton=1`).Scan(&installation); err != nil {
		return operation.Operation{}, err
	}
	intent := workspace.LaunchIntent{
		InstallationID: installation, OwnerID: actor, RequestKey: input.key,
		TemplateID: input.TemplateID, TemplateRevision: input.TemplateRevision, RetentionSeconds: input.RetentionSeconds,
	}
	if op, found, err := workspace.FindLaunch(ctx, tx, journal, intent); found || err != nil {
		return op, launchAdmissionProblem(err)
	}
	if input.RetentionSeconds != nil && (policy.RetentionMaximum == nil || *input.RetentionSeconds > *policy.RetentionMaximum ||
		workspace.RetentionDeadline(now, input.RetentionSeconds) == nil) {
		return operation.Operation{}, Problem{Status: 409, Code: "workspace.retention_unavailable", Title: "Choose an allowed retention period"}
	}
	var allocation workspace.Allocation
	var architecture, protocol string
	err := tx.QueryRowContext(ctx, `SELECT p.cpu_count,p.memory_mib,p.disk_mib,a.architecture,a.protocol_version
		FROM resource_profiles p CROSS JOIN platform_artifacts a
		WHERE p.id=$1 AND p.enabled=1 AND a.id=$2 AND a.state='available'`, revision.ProfileID, revision.ArtifactID).
		Scan(&allocation.CPU, &allocation.MemoryMiB, &allocation.DiskMiB, &architecture, &protocol)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, Problem{Status: 409, Code: "workspace.template_unavailable", Title: "The template's profile or platform release is unavailable"}
	}
	if err != nil {
		return operation.Operation{}, err
	}
	caps := policy.Capabilities
	if !caps.Create || !caps.Start || caps.Architecture != architecture || !slices.Contains(policy.Protocols, protocol) {
		return operation.Operation{}, launchCapabilityProblem()
	}
	network, err := workspace.LaunchNetworkSnapshot(ctx, tx, revision.NetworkPolicyID, caps)
	if errors.Is(err, provider.ErrCapabilityUnsupported) {
		return operation.Operation{}, launchCapabilityProblem()
	}
	if err != nil {
		return operation.Operation{}, err
	}
	if _, err := workspace.ProfileResources(allocation, caps); err != nil {
		return operation.Operation{}, launchCapabilityProblem()
	}
	if revision.RepositoryRef != "" {
		intent.RepositoryRef = &revision.RepositoryRef
	}
	intent.WorkspaceID = uuid.NewString()
	intent.OwnershipMarker = uuid.NewString()
	intent.ArtifactID = revision.ArtifactID
	intent.RepositoryURL = revision.RepositoryURL
	intent.DevcontainerPath = revision.DevcontainerPath
	intent.Allocation = allocation
	intent.NetworkPolicyJSON = network
	intent.SecretNames = revision.SecretNames
	op, err := workspace.AdmitLaunch(ctx, tx, journal, intent, now)
	return op, launchAdmissionProblem(err)
}

func launchAdmissionProblem(err error) error {
	var capacity *workspace.CapacityError
	switch {
	case errors.As(err, &capacity):
		return Problem{Status: 409, Code: "workspace.capacity_exceeded", Title: "There is insufficient " + capacity.Resource + " capacity", Fields: map[string]string{"resource": capacity.Resource}}
	case errors.Is(err, workspace.ErrLaunchConflict):
		return Problem{Status: 409, Code: "workspace.launch_conflict", Title: "This request identifier is already in use"}
	case errors.Is(err, workspace.ErrCapacityConflict):
		return Problem{Status: 409, Code: "workspace.admission_unavailable", Title: "Workspace launch is unavailable"}
	}
	return err
}

func launchCapabilityProblem() Problem {
	return Problem{Status: 409, Code: "workspace.capability_unavailable", Title: "The provider cannot launch this template"}
}
