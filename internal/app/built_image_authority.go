package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/template"
	"github.com/jflatten/ShiftBox/internal/web"
)

type builtImageAuthority struct {
	installationID, projectID, ownerID, workspaceID string
	repositoryURL, repositoryRef, configPath        string
	targetOS, targetArch, operationID, kind         string
	generation, policyGeneration, projectRevision   int64
	dispatchVersion                                 int64
}

// loadBuiltImageAuthority derives all image scope from a currently enrolled
// workspace and its project. Only an already admitted transfer can continue
// while the IDE and remaining hooks finish or after the operation succeeds.
func loadBuiltImageAuthority(ctx context.Context, tx *sql.Tx, contact guest.Contact, operationID string, dispatchVersion int64, allowContinuation bool) (builtImageAuthority, error) {
	current := builtImageAuthority{
		workspaceID: contact.WorkspaceID, generation: contact.Generation, ownerID: contact.OwnerID,
		targetOS: "linux", operationID: operationID, dispatchVersion: dispatchVersion,
	}
	var role, workspaceRepository, workspaceConfigPath, operationType string
	err := tx.QueryRowContext(ctx, `SELECT w.installation_id,w.template_id,t.current_revision,i.built_image_generation,
		p.architecture,u.role,w.repository_url,w.devcontainer_path,o.type
		FROM workspaces w
		JOIN installations i ON i.id=w.installation_id
		JOIN users u ON u.id=w.owner_user_id
		JOIN project_templates t ON t.id=w.template_id
		JOIN platform_artifacts p ON p.id=w.platform_artifact_id
		JOIN guest_registrations r ON r.workspace_id=w.id AND r.generation=w.generation
		JOIN operations o ON o.workspace_id=w.id AND o.generation=w.generation
		JOIN guest_dispatches g ON g.operation_id=o.id
		JOIN operation_steps s ON s.operation_id=o.id AND s.step_key='configure'
		WHERE w.id=$1 AND w.generation=$2 AND o.id=$3 AND g.dispatch_version=$4 AND w.owner_user_id=$6
		AND w.desired_state='running' AND w.provider_instance_id IS NOT NULL AND w.deletion_commit_at IS NULL
		AND i.state='active' AND i.built_image_enabled=1 AND u.membership_state='active' AND t.state='active' AND r.state='active'
		AND ((o.type='provision' AND o.originating_action='workspace.launch') OR
		     (o.type='rebuild' AND o.originating_action='workspace.rebuild'))
	AND ((o.status IN ('running','waiting') AND g.status IN ('pending','running') AND s.status IN ('active','waiting'))
	     OR ($5 AND o.status IN ('running','waiting') AND g.status='running' AND s.status='succeeded'
	         AND o.current_step IN ('verify_ide','finish_setup'))
	     OR ($5 AND o.status IN ('running','waiting','succeeded') AND g.status='succeeded'))
		AND NOT EXISTS (SELECT 1 FROM operations newer JOIN guest_dispatches d ON d.operation_id=newer.id
			WHERE newer.workspace_id=w.id AND newer.generation=w.generation AND newer.created_at>o.created_at)
		`, contact.WorkspaceID, contact.Generation, operationID, dispatchVersion, allowContinuation, contact.OwnerID).
		Scan(&current.installationID, &current.projectID, &current.projectRevision, &current.policyGeneration,
			&current.targetArch, &role, &workspaceRepository, &workspaceConfigPath, &operationType)
	if errors.Is(err, sql.ErrNoRows) {
		return builtImageAuthority{}, guest.ErrGuestUnauthorized
	}
	if err != nil {
		return builtImageAuthority{}, err
	}
	if current.targetArch != "amd64" && current.targetArch != "arm64" {
		return builtImageAuthority{}, guest.ErrGuestUnauthorized
	}
	revision, err := template.ResolveLaunchRevision(ctx, tx, current.projectID, current.projectRevision, current.ownerID)
	if err != nil {
		return builtImageAuthority{}, err
	}
	// Guest enrollment establishes active membership. Project owners and
	// administrators have launch access without an explicit sharing grant.
	if role != "administrator" && (role != "member" || revision.Resource.OwnerID != current.ownerID && !revision.Resource.LaunchGranted) {
		return builtImageAuthority{}, guest.ErrGuestUnauthorized
	}
	configuredRepository, valid := identity.CanonicalGitHubRepository(revision.RepositoryURL)
	pinnedRepository, pinnedValid := identity.CanonicalGitHubRepository(workspaceRepository)
	if !valid || !pinnedValid || configuredRepository != pinnedRepository || workspaceConfigPath != revision.DevcontainerPath {
		return builtImageAuthority{}, guest.ErrGuestUnauthorized
	}
	current.repositoryURL, current.repositoryRef, current.configPath = revision.RepositoryURL, revision.RepositoryRef, revision.DevcontainerPath
	current.kind = "launch"
	if operationType == "rebuild" {
		current.kind = "rebuild"
	}
	return current, nil
}

func validateBuiltImageAuthority(current, expected builtImageAuthority, sourceVersion, expectedSourceVersion int64) error {
	if current != expected || sourceVersion < 1 || sourceVersion != expectedSourceVersion {
		return guest.ErrGuestUnauthorized
	}
	return nil
}

// registerBuiltImageTransfer checks authority under the existing connection
// admission lock. Revocations commit first and cancel matching operations there.
func (a builtImageAuthority) registerBuiltImageTransfer(ctx context.Context, registry *proxy.Registry, grantID string, expiry time.Time, authorize func(context.Context) error) (context.Context, func(), error) {
	transfer, cancel := context.WithDeadline(ctx, expiry)
	unregister, err := registry.Register(proxy.Scope{
		UserID: a.ownerID, WorkspaceID: a.workspaceID, ProjectID: a.projectID, GrantID: grantID,
		BuiltImageGeneration: a.policyGeneration,
	}, func() error {
		if err := transfer.Err(); err != nil {
			return err
		}
		return authorize(transfer)
	}, func() error {
		cancel()
		return nil
	})
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return transfer, func() {
		unregister()
		cancel()
	}, nil
}

// fenceBuiltImageRevocation closes authorized streams after a GitHub
// disconnect, project archival, or sharing revocation commits.
func fenceBuiltImageRevocation(handler web.AuthorizedHandler, registry *proxy.Registry) web.AuthorizedHandler {
	apply := handler.Apply
	handler.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, target web.AuthorizationTarget, decision auth.Decision) (http.Handler, error) {
		response, err := apply(ctx, tx, r, principal, target, decision)
		if err != nil {
			return nil, err
		}
		var userID, projectID string
		switch handler.Route.OperationID {
		case "revokeGitAuthorization":
			userID = principal.User.ID
		case "revokeTemplateLaunch":
			userID, projectID = r.PathValue("userId"), target.Resource.ID
		case "updateTemplate":
			if target.Action == "template.archive" {
				projectID = target.Resource.ID
			}
		}
		if userID == "" && projectID == "" {
			return response, nil
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := registry.CloseMatching(func(scope proxy.Scope) bool {
				return scope.BuiltImageGeneration > 0 &&
					(userID == "" || scope.UserID == userID) && (projectID == "" || scope.ProjectID == projectID)
			})
			if err != nil {
				web.WriteProblem(w, web.RequestID(r.Context()), err)
				return
			}
			response.ServeHTTP(w, r)
		}), nil
	}
	return handler
}
