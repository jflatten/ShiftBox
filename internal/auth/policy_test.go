package auth_test

import (
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
)

func activePrincipal() auth.Principal {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	return auth.Principal{User: auth.User{ID: "alice", Role: "member", MembershipState: "active", CredentialMode: "local"}, Session: auth.Session{UserID: "alice", Mode: "normal", AuthenticatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}, InstallationState: "active", ExternalAuthInterval: 4 * time.Hour}
}

// TestRoleAndOwnershipMatrix keeps lifecycle administration separate from private workspace entry.
func TestRoleAndOwnershipMatrix(t *testing.T) {
	for _, test := range []struct {
		action, kind        string
		owner, other, admin bool
	}{
		{"workspace.view", "workspace", true, false, true},
		{"workspace.start", "workspace", true, false, true},
		{"workspace.stop", "workspace", true, false, true},
		{"workspace.rebuild", "workspace", true, false, true},
		{"workspace.repair", "workspace", true, false, true},
		{"workspace.delete", "workspace", true, false, true},
		{"workspace.update", "workspace", true, false, true},
		{"workspace.update_policy", "workspace", true, false, true},
		{"workspace.connect_owner", "workspace", true, false, false},
		{"workspace.connect_admin", "workspace", false, false, false},
		{"template.view", "template", true, false, true},
		{"template.edit", "template", true, false, true},
		{"template.create_revision", "template", true, false, true},
		{"template.share", "template", true, false, true},
		{"template.archive", "template", true, false, true},
		{"secret.manage_user", "user", true, false, false},
		{"secret.manage_template", "template", true, false, true},
		{"secret.manage_organization", "organization", false, false, true},
		{"git.manage", "user", true, false, false},
		{"secret.resolve", "workspace", false, false, false},
		{"git.resolve", "workspace", false, false, false},
		{"infrastructure.manage", "installation", false, false, true},
		{"infrastructure.view", "installation", false, false, true},
		{"maintenance.manage", "installation", false, false, true},
		{"migration.preflight", "installation", false, false, true},
		{"backup.create", "installation", false, false, true},
		{"restore.validate", "installation", false, false, true},
		{"restore.activate", "installation", false, false, true},
		{"user.list", "user", false, false, true},
		{"user.create", "user", false, false, true},
		{"user.approve", "user", false, false, true},
		{"user.disable", "user", false, false, true},
		{"user.change_role", "user", false, false, true},
		{"notice.publish", "installation", false, false, true},
		{"audit.list", "installation", false, false, true},
		{"notice.list", "installation", true, true, true},
		{"notice.read", "installation", true, true, true},
		{"notice.dismiss", "installation", true, true, true},
		{"template.create", "template", true, true, true},
	} {
		// Check the same action as owner, unrelated member, and unrelated administrator.
		t.Run(test.action, func(t *testing.T) {
			p := activePrincipal()
			resource := auth.Resource{Type: test.kind, ID: "resource", OwnerID: "alice", State: "active", Eligible: true}
			for _, scenario := range []struct {
				role, owner string
				want        bool
			}{{"member", "alice", test.owner}, {"member", "bob", test.other}, {"administrator", "bob", test.admin}} {
				p.User.Role = scenario.role
				resource.OwnerID = scenario.owner
				if test.kind == "user" {
					resource.ID = scenario.owner
				}
				got := auth.Authorize(p, test.action, resource, nil, time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC))
				if got.Allowed != scenario.want {
					t.Errorf("Authorize(%s,role=%s,owner=%s) = %t, want %t", test.action, scenario.role, scenario.owner, got.Allowed, scenario.want)
				}
			}
		})
	}
}

// TestRestrictedSessionActions keeps recovery and bootstrap sessions within their own workflow.
func TestRestrictedSessionActions(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	p := activePrincipal()
	p.User.Role = "administrator"
	p.Session.Mode = "recovery_review"
	p.InstallationState = "recovery"
	if !auth.Authorize(p, "recovery.review", auth.Resource{}, nil, now).Allowed {
		t.Error("recovery administrator cannot review")
	}
	if auth.Authorize(p, "recovery.activate", auth.Resource{}, nil, now).Allowed {
		t.Error("unreviewed recovery can activate")
	}
	p.RecoveryReady = true
	if !auth.Authorize(p, "recovery.activate", auth.Resource{}, nil, now).Allowed {
		t.Error("reviewed recovery cannot activate")
	}
	p.Session.Mode = "bootstrap_restricted"
	p.InstallationState = "bootstrap"
	if auth.Authorize(p, "setup.mutate", auth.Resource{}, nil, now).Allowed {
		t.Error("remote bootstrap mutation accepted")
	}
	p.LocalBootstrap = true
	if !auth.Authorize(p, "setup.mutate", auth.Resource{}, nil, now).Allowed {
		t.Error("protected local bootstrap denied")
	}
	p.Session.Mode = "password_change"
	p.User.MustChangePassword = true
	if !auth.Authorize(p, "session.change_password", auth.Resource{}, nil, now).Allowed {
		t.Error("forced password change denied")
	}
}

// TestRecoveryAndReauthorizationGateEveryAction prevents helper routes bypassing installation gates.
func TestRecoveryAndReauthorizationGateEveryAction(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	p := activePrincipal()
	p.InstallationState = "recovery"
	if auth.Authorize(p, "session.change_password", auth.Resource{}, nil, now).Allowed {
		t.Error("normal password mutation bypassed recovery gate")
	}
	p = activePrincipal()
	p.User.Role = "administrator"
	p.User.CredentialMode = "external"
	p.InstallationState = "blocked"
	past := now.Add(-5 * time.Hour)
	p.Session.ExternalAuthorizedAt = &past
	if auth.Authorize(p, "infrastructure.view", auth.Resource{}, nil, now).Allowed {
		t.Error("blocked status view bypassed upstream reauthorization deadline")
	}
	p = activePrincipal()
	p.Session.Mode = "unknown"
	if auth.Authorize(p, "session.view", auth.Resource{}, nil, now).Allowed {
		t.Error("unknown session mode accepted")
	}
}

// TestAdminConnectionRequiresExactGrant prevents a grant for one workspace opening another.
func TestAdminConnectionRequiresExactGrant(t *testing.T) {
	p := activePrincipal()
	p.User.Role = "administrator"
	resource := auth.Resource{Type: "workspace", ID: "workspace-one", OwnerID: "bob", Eligible: true}
	grant := auth.ResourceGrant{SubjectUserID: "alice", ResourceType: "workspace", ResourceID: "workspace-one", Action: "workspace.connect_admin"}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	got := auth.Authorize(p, "workspace.connect_admin", resource, []auth.ResourceGrant{grant}, now)
	if !got.Allowed || !got.AuditRequired || got.OwnerContext != "bob" {
		t.Errorf("admin connection = %+v, want audited access in bob's context", got)
	}
	grant.ResourceID = "another-workspace"
	if auth.Authorize(p, "workspace.connect_admin", resource, []auth.ResourceGrant{grant}, now).Allowed {
		t.Error("cross-resource admin grant accepted")
	}
	grant.ResourceID = resource.ID
	grant.RevokedAt = &now
	if auth.Authorize(p, "workspace.connect_admin", resource, []auth.ResourceGrant{grant}, now).Allowed {
		t.Error("revoked admin grant accepted")
	}
}

// TestAuthorityGates denies normal actions after expiry, disablement, or entry into recovery.
func TestAuthorityGates(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	resource := auth.Resource{Type: "workspace", ID: "one", OwnerID: "alice", Eligible: true}
	for _, change := range []func(*auth.Principal){
		func(p *auth.Principal) { p.User.MembershipState = "disabled" },
		func(p *auth.Principal) { p.User.MembershipState = "pending" },
		func(p *auth.Principal) { p.Session.ExpiresAt = now },
		func(p *auth.Principal) { p.Session.RevokedAt = &now },
		func(p *auth.Principal) { p.Session.UserID = "bob" },
		func(p *auth.Principal) { p.Session.Mode = "password_change" },
		func(p *auth.Principal) { p.User.MustChangePassword = true },
		func(p *auth.Principal) { p.InstallationState = "recovery" },
		func(p *auth.Principal) { p.InstallationState = "blocked" },
		func(p *auth.Principal) {
			p.User.CredentialMode = "external"
			past := now.Add(-4 * time.Hour)
			p.Session.ExternalAuthorizedAt = &past
		},
	} {
		p := activePrincipal()
		change(&p)
		if auth.Authorize(p, "workspace.connect_owner", resource, nil, now).Allowed {
			t.Error("ineligible principal retained workspace access")
		}
	}
}

// TestTemplateGrantDoesNotShareWorkspace preserves private launches from shared templates.
func TestTemplateGrantDoesNotShareWorkspace(t *testing.T) {
	p := activePrincipal()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	template := auth.Resource{Type: "template", ID: "shared", OwnerID: "bob", State: "active", LaunchGranted: true}
	if !auth.Authorize(p, "workspace.launch", template, nil, now).Allowed {
		t.Error("authorized shared template cannot launch")
	}
	template.State = "archived"
	if auth.Authorize(p, "workspace.launch", template, nil, now).Allowed {
		t.Error("archived template can launch")
	}
	workspace := auth.Resource{Type: "workspace", ID: "private", OwnerID: "bob", Eligible: true, LaunchGranted: true}
	if auth.Authorize(p, "workspace.connect_owner", workspace, nil, now).Allowed {
		t.Error("template sharing opened another owner's workspace")
	}
}
