package web

import (
	"errors"
	"github.com/jflatten/ShiftBox/internal/auth"
	"strings"
	"unicode/utf8"
)

// SecretScopeTarget binds request scope identity to the record loaded by the
// controller. Policy still checks current ownership and role for this resource.
func SecretScopeTarget(scope, id string, resource auth.Resource) (AuthorizationTarget, error) {
	switch scope {
	case "user", "template", "organization":
	default:
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	if id == "" || resource.Type != scope || resource.ID != id {
		return AuthorizationTarget{}, errors.New("secret scope does not match loaded resource")
	}
	return AuthorizationTarget{Action: "secret.manage_" + scope, Resource: resource}, nil
}

// NoticeReceiptTarget selects the action for the current user's receipt. The
// endpoint must bind the receipt's user to the authenticated principal, never an
// arbitrary request field. Persistent system conditions are unaffected.
func NoticeReceiptTarget(state, noticeID string) (AuthorizationTarget, error) {
	if noticeID == "" {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	action := ""
	switch state {
	case "read":
		action = "notice.read"
	case "dismissed":
		action = "notice.dismiss"
	default:
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	return AuthorizationTarget{Action: action, Resource: auth.Resource{Type: "notice", ID: noticeID}}, nil
}

// TemplateChangeTarget distinguishes archiving from editing using the decoded
// request fields, retaining the loaded template's ownership for policy checks.
func TemplateChangeTarget(resource auth.Resource, name, state *string) (AuthorizationTarget, error) {
	if resource.Type != "template" || resource.ID == "" {
		return AuthorizationTarget{}, errors.New("template authorization context is missing")
	}
	if name == nil && state == nil {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	if name != nil && (strings.TrimSpace(*name) == "" || utf8.RuneCountInString(*name) > 120) {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	action := "template.edit"
	if state != nil {
		switch *state {
		case "active":
		case "archived":
			action = "template.archive"
		default:
			return AuthorizationTarget{}, invalidAuthorizationFields()
		}
	}
	return AuthorizationTarget{Action: action, Resource: resource}, nil
}

// MembershipChangeTarget maps pending/disabled to access removal, active to
// approval, and role-only changes to role administration. Combined changes use
// the membership action; all three actions require administrator authority and
// cannot be granted through resource grants in the approved MVP policy.
func MembershipChangeTarget(userID string, role, state *string) (AuthorizationTarget, error) {
	if userID == "" || (role == nil && state == nil) {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	if role != nil && *role != "member" && *role != "administrator" {
		return AuthorizationTarget{}, invalidAuthorizationFields()
	}
	action := "user.change_role"
	if state != nil {
		switch *state {
		case "active":
			action = "user.approve"
		case "pending", "disabled":
			action = "user.disable"
		default:
			return AuthorizationTarget{}, invalidAuthorizationFields()
		}
	}
	return AuthorizationTarget{Action: action, Resource: auth.Resource{Type: "user", ID: userID}}, nil
}

func invalidAuthorizationFields() error {
	return Problem{Status: 400, Code: "request.invalid_fields", Title: "Check the requested change"}
}
