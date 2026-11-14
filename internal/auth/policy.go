package auth

import "time"

// Authorize evaluates the approved role/resource matrix. Callers must read these
// records and commit protected intent in the same database transaction.
func Authorize(p Principal, action string, resource Resource, grants []ResourceGrant, now time.Time) Decision {
	deny := Decision{}
	allow := Decision{Allowed: true, OwnerContext: resource.OwnerID}
	if p.User.ID == "" || p.User.MembershipState != "active" || p.Session.UserID != p.User.ID || p.Session.RevokedAt != nil || !now.Before(p.Session.ExpiresAt) || now.Before(p.Session.AuthenticatedAt) {
		return deny
	}
	admin := p.User.Role == "administrator"
	if !admin && p.User.Role != "member" {
		return deny
	}
	switch p.Session.Mode {
	case "normal", "password_change", "recovery_review":
	case "bootstrap_restricted":
		if !p.LocalBootstrap {
			return deny
		}
	default:
		return deny
	}
	if action == "session.view" || action == "session.logout" {
		return allow
	}
	passwordState := p.InstallationState == "active" || ((p.InstallationState == "bootstrap" || p.InstallationState == "configuring") && p.LocalBootstrap)
	if passwordState && action == "session.change_password" && p.User.CredentialMode == "local" && (p.Session.Mode == "normal" || p.Session.Mode == "password_change" || p.Session.Mode == "bootstrap_restricted") {
		return allow
	}
	if p.User.MustChangePassword || p.Session.Mode == "password_change" {
		return deny
	}
	if p.Session.Mode == "bootstrap_restricted" {
		if p.LocalBootstrap && (p.InstallationState == "bootstrap" || p.InstallationState == "configuring") && (action == "setup.view" || action == "setup.mutate") {
			return allow
		}
		return deny
	}
	if p.Session.Mode == "recovery_review" {
		if admin && p.InstallationState == "recovery" && (action == "recovery.review" || (action == "recovery.activate" && p.RecoveryReady)) {
			return allow
		}
		return deny
	}
	if p.Session.Mode != "normal" {
		return deny
	}
	if p.User.CredentialMode == "external" {
		if action == "session.external_reauthorize" && (p.InstallationState == "active" || p.InstallationState == "blocked") {
			return allow
		}
		if p.ExternalAuthInterval <= 0 || p.Session.ExternalAuthorizedAt == nil || p.Session.ExternalAuthorizedAt.After(now) || !now.Before(p.Session.ExternalAuthorizedAt.Add(p.ExternalAuthInterval)) {
			return deny
		}
	} else if p.User.CredentialMode != "local" {
		return deny
	}
	if p.InstallationState == "blocked" {
		if admin && (action == "infrastructure.view" || action == "migration.preflight") {
			return allow
		}
		return deny
	}
	if p.InstallationState != "active" {
		return deny
	}
	owner := resource.OwnerID == p.User.ID
	switch action {
	case "workspace.list", "workspace.view", "workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair", "workspace.delete", "workspace.update", "workspace.update_policy":
		if resource.Type == "workspace" && (owner || admin) {
			return allow
		}
	case "workspace.connect_owner":
		if resource.Type == "workspace" && owner && resource.Eligible {
			return allow
		}
	case "workspace.connect_admin":
		if !admin || resource.Type != "workspace" || !resource.Eligible {
			return deny
		}
		for _, grant := range grants {
			if grant.SubjectUserID == p.User.ID && grant.ResourceType == "workspace" && grant.ResourceID == resource.ID && grant.Action == action && grant.RevokedAt == nil {
				allow.AuditRequired = true
				return allow
			}
		}
	case "workspace.launch":
		if resource.Type == "template" && resource.State == "active" && (owner || admin || resource.LaunchGranted) {
			return allow
		}
	case "template.create", "notice.list", "notice.read", "notice.dismiss":
		return allow
	case "template.view":
		if resource.Type == "template" && (admin || (resource.State == "active" && (owner || resource.LaunchGranted))) {
			return allow
		}
	case "template.edit", "template.create_revision", "template.share", "template.archive", "secret.manage_template":
		if resource.Type == "template" && (owner || admin) {
			return allow
		}
	case "secret.manage_user", "git.manage":
		if resource.Type == "user" && resource.ID == p.User.ID {
			return allow
		}
	case "secret.manage_organization":
		if admin && resource.Type == "organization" {
			return allow
		}
	case "user.list", "user.create", "user.approve", "user.disable", "user.change_role", "notice.publish", "audit.list", "infrastructure.view", "infrastructure.manage", "maintenance.manage", "backup.create", "restore.validate", "restore.activate", "migration.preflight":
		if admin {
			return allow
		}
	}
	return deny
}
