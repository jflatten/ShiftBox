package web

import (
	"errors"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// WorkspaceAuthorization is loaded by the endpoint in its authority transaction.
// LaunchTemplate must be the workspace's captured template; FailedOperation must
// be its persisted failed operation. None of these records comes from request JSON.
type WorkspaceAuthorization struct {
	Workspace       auth.Resource
	LaunchTemplate  auth.Resource
	FailedOperation *operation.Operation
	Grants          []auth.ResourceGrant
}

// Action resolves the public lifecycle verb or the persisted failed operation's
// original permission. Retry does not become a new blanket workspace permission.
func (c WorkspaceAuthorization) Action(principal auth.Principal, action string) (AuthorizationTarget, error) {
	if c.Workspace.Type != "workspace" || c.Workspace.ID == "" || c.Workspace.OwnerID == "" {
		return AuthorizationTarget{}, errors.New("workspace authorization context is missing")
	}
	if action == "retry" {
		failed := c.FailedOperation
		if failed == nil || failed.Status != "failed" || failed.WorkspaceID == nil || *failed.WorkspaceID != c.Workspace.ID {
			return AuthorizationTarget{}, Problem{Status: 409, Code: "operation.retry_unavailable", Title: "This operation cannot be retried"}
		}
		switch failed.Type {
		case "provision":
			// A shared template permits a new workspace, not control of an
			// existing workspace owned by another member.
			if principal.User.ID != c.Workspace.OwnerID && principal.User.Role != "administrator" {
				return AuthorizationTarget{}, Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
			}
			if c.LaunchTemplate.Type != "template" || c.LaunchTemplate.ID == "" {
				return AuthorizationTarget{}, errors.New("retry launch template is missing")
			}
			return AuthorizationTarget{Action: "workspace.launch", Resource: c.LaunchTemplate}, nil
		case "open_ide":
			return c.Entry(principal)
		case "start", "stop", "rebuild", "repair", "delete", "update":
			action = failed.Type
		default:
			return AuthorizationTarget{}, Problem{Status: 409, Code: "operation.retry_unavailable", Title: "This operation cannot be retried"}
		}
	}
	switch action {
	case "start", "stop", "rebuild", "repair", "delete", "update":
		return AuthorizationTarget{Action: "workspace." + action, Resource: c.Workspace, Grants: c.Grants}, nil
	default:
		return AuthorizationTarget{}, Problem{Status: 400, Code: "workspace.invalid_action", Title: "Choose a supported workspace action"}
	}
}

// Entry selects owner or administrator connection permission without granting
// it. Authorize still requires current membership, eligibility, and (for an
// administrator) an exact active grant followed by a durable access audit.
func (c WorkspaceAuthorization) Entry(principal auth.Principal) (AuthorizationTarget, error) {
	if principal.User.ID == "" || c.Workspace.Type != "workspace" || c.Workspace.ID == "" || c.Workspace.OwnerID == "" {
		return AuthorizationTarget{}, errors.New("workspace entry context is missing")
	}
	action := "workspace.connect_admin"
	if c.Workspace.OwnerID == principal.User.ID {
		action = "workspace.connect_owner"
	}
	return AuthorizationTarget{Action: action, Resource: c.Workspace, Grants: c.Grants}, nil
}
