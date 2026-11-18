package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
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
	// EC2VerifiedAMIs are the deployment-approved images for guest enrollment.
	EC2VerifiedAMIs  []string
	EC2SingleBootAMI string
	// VerifyNetwork observes the provider's supported stopped-VM network mode.
	VerifyNetwork func(context.Context, provider.NetworkRequest) (provider.Inventory, error)
	// AvailableDisk measures the provider volume before a new allocation.
	AvailableDisk func() (uint64, error)
}

type workspaceLaunchRequest struct {
	TemplateID       string  `json:"templateId"`
	TemplateRevision int64   `json:"templateRevision"`
	Name             *string `json:"name"`
	RetentionSeconds *int64  `json:"retentionSeconds"`
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
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || !workspace.ValidName(name) {
			return AuthorizationTarget{}, invalidAuthorizationFields()
		}
		input.Name = &name
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
	var installation, currentGuest, previousGuest string
	if err := tx.QueryRowContext(ctx, `SELECT id,active_release,min_guest_release FROM installations WHERE singleton=1`).
		Scan(&installation, &currentGuest, &previousGuest); err != nil {
		return operation.Operation{}, err
	}
	intent := workspace.LaunchIntent{
		InstallationID: installation, OwnerID: actor, RequestKey: input.key,
		TemplateID: input.TemplateID, TemplateRevision: input.TemplateRevision, RetentionSeconds: input.RetentionSeconds,
	}
	if input.Name != nil {
		intent.Name = *input.Name
	}
	if op, found, err := workspace.FindLaunch(ctx, tx, journal, intent); found || err != nil {
		return op, launchAdmissionProblem(err)
	}
	if input.RetentionSeconds != nil && (policy.RetentionMaximum == nil || *input.RetentionSeconds > *policy.RetentionMaximum ||
		workspace.RetentionDeadline(now, input.RetentionSeconds) == nil) {
		return operation.Operation{}, Problem{Status: 409, Code: "workspace.retention_unavailable", Title: "Choose an allowed retention period"}
	}
	var allocation workspace.Allocation
	var profileImage, amiFamily string
	caps := policy.Capabilities
	intent.ProviderKind = launchProviderKind(caps)
	err := tx.QueryRowContext(ctx, `SELECT cpu_count,memory_mib,disk_mib,instance_type,COALESCE(platform_artifact_id,''),ec2_ami_family FROM resource_profiles
		WHERE id=$1 AND enabled=1`, revision.ProfileID).
		Scan(&allocation.CPU, &allocation.MemoryMiB, &allocation.DiskMiB, &allocation.InstanceType, &profileImage, &amiFamily)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, templateLaunchUnavailable()
	}
	if err != nil {
		return operation.Operation{}, err
	}
	if !caps.Create || !caps.Start {
		return operation.Operation{}, launchCapabilityProblem()
	}
	artifactID := revision.ArtifactID
	if intent.ProviderKind == "ec2" {
		artifactID = profileImage
		instance, ok := caps.LookupInstanceType(allocation.EC2Type())
		if !ok {
			return operation.Operation{}, launchCapabilityProblem()
		}
		var singleBoot bool
		if amiFamily != "" {
			artifactID, err = selectEC2FamilyImage(ctx, tx, policy, amiFamily, instance.Architecture, allocation.DiskMiB, currentGuest, previousGuest)
			singleBoot = true
		} else {
			singleBoot, err = verifyEC2LaunchImage(ctx, tx, policy, artifactID, instance.Architecture, currentGuest, previousGuest)
		}
		if errors.Is(err, sql.ErrNoRows) {
			if amiFamily != "" {
				return operation.Operation{}, Problem{Status: 409, Code: "workspace.platform_incompatible",
					Title: "The AMI family has no compatible image", Detail: "Ask an infrastructure administrator to publish a validated image compatible with the resource profile."}
			}
			return operation.Operation{}, Problem{Status: 409, Code: "workspace.platform_incompatible",
				Title:  "The resource profile needs a compatible AMI",
				Detail: "Ask an infrastructure administrator to select a verified " + instance.Architecture + " AMI on the resource profile, then launch again."}
		}
		if err != nil {
			return operation.Operation{}, err
		}
		intent.SingleBoot = singleBoot
	} else {
		var architecture, protocol, guestRelease string
		err := tx.QueryRowContext(ctx, `SELECT architecture,protocol_version,guest_release FROM platform_artifacts
			WHERE id=$1 AND state='available' AND provider_kind='utm'`, artifactID).
			Scan(&architecture, &protocol, &guestRelease)
		if errors.Is(err, sql.ErrNoRows) {
			return operation.Operation{}, templateLaunchUnavailable()
		}
		if err != nil {
			return operation.Operation{}, err
		}
		if guestRelease == "" || (guestRelease != currentGuest && guestRelease != previousGuest) {
			return operation.Operation{}, Problem{Status: 409, Code: "workspace.platform_incompatible", Title: "The project's platform release is incompatible with this controller"}
		}
		if !caps.SupportsArchitecture(architecture) || !slices.Contains(policy.Protocols, protocol) {
			return operation.Operation{}, launchCapabilityProblem()
		}
	}
	network, err := workspace.LaunchNetworkSnapshot(ctx, tx, revision.NetworkPolicyID, caps)
	if errors.Is(err, provider.ErrCapabilityUnsupported) {
		return operation.Operation{}, launchCapabilityProblem()
	}
	if err != nil {
		return operation.Operation{}, err
	}
	resources, err := workspace.ProfileResources(allocation, caps)
	if err != nil {
		return operation.Operation{}, launchCapabilityProblem()
	}
	if policy.AvailableDisk != nil {
		available, err := policy.AvailableDisk()
		if err != nil {
			return operation.Operation{}, Problem{Status: 503, Code: "workspace.storage_unavailable", Title: "Cannot verify available space on the workspace volume"}
		}
		if available < uint64(resources.DiskBytes) {
			return operation.Operation{}, Problem{Status: 409, Code: "workspace.capacity_exceeded", Title: "There is insufficient disk space on the workspace volume", Fields: map[string]string{"resource": "disk"}}
		}
	}
	if revision.RepositoryRef != "" {
		intent.RepositoryRef = &revision.RepositoryRef
	}
	intent.WorkspaceID = uuid.NewString()
	intent.OwnershipMarker = uuid.NewString()
	intent.ArtifactID = artifactID
	intent.RepositoryURL = revision.RepositoryURL
	intent.DevcontainerPath = revision.DevcontainerPath
	intent.Allocation = allocation
	intent.NetworkPolicyJSON = network
	intent.SecretNames = revision.SecretNames
	op, err := workspace.AdmitLaunch(ctx, tx, journal, intent, now)
	return op, launchAdmissionProblem(err)
}

func templateLaunchUnavailable() Problem {
	return Problem{Status: 409, Code: "workspace.template_unavailable", Title: "The project's profile or platform release is unavailable"}
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
	case errors.Is(err, provider.ErrCapabilityUnsupported):
		return launchCapabilityProblem()
	}
	return err
}

func launchCapabilityProblem() Problem {
	return Problem{Status: 409, Code: "workspace.capability_unavailable", Title: "The provider cannot launch this project"}
}
