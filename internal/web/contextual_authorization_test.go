package web_test

import (
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/web"
	"testing"
)

// TestSecretScopeSelection binds the requested scope to the loaded authority record and never creates a workspace secret scope.
func TestSecretScopeSelection(t *testing.T) {
	for _, scope := range []string{"user", "template", "organization"} {
		resource := auth.Resource{Type: scope, ID: "record", OwnerID: "owner"}
		target, err := web.SecretScopeTarget(scope, "record", resource)
		if err != nil || target.Action != "secret.manage_"+scope || target.Resource != resource {
			t.Errorf("SecretScopeTarget(%q) = %+v, %v", scope, target, err)
		}
		if _, err := web.SecretScopeTarget(scope, "different", resource); err == nil {
			t.Errorf("%s accepted mismatched scope ID", scope)
		}
	}
	for _, scope := range []string{"workspace", "", "USER"} {
		if _, err := web.SecretScopeTarget(scope, "record", auth.Resource{Type: scope, ID: "record"}); err == nil {
			t.Errorf("unsupported secret scope %q accepted", scope)
		}
	}
	if _, err := web.SecretScopeTarget("user", "record", auth.Resource{Type: "template", ID: "record"}); err == nil {
		t.Error("cross-type scope accepted")
	}
}

// TestNoticeReceiptSelection prevents unrecognized receipt values from silently dismissing a notice.
func TestNoticeReceiptSelection(t *testing.T) {
	for _, tc := range []struct{ state, action string }{{"read", "notice.read"}, {"dismissed", "notice.dismiss"}, {"", ""}, {"deleted", ""}} {
		target, err := web.NoticeReceiptTarget(tc.state, "notice")
		if tc.action == "" {
			if err == nil {
				t.Errorf("NoticeReceiptTarget(%q) succeeded", tc.state)
			}
			continue
		}
		if err != nil || target.Action != tc.action || target.Resource.ID != "notice" {
			t.Errorf("NoticeReceiptTarget(%q) = %+v, %v", tc.state, target, err)
		}
	}
}

// TestTemplateChangeSelection ensures an archive request cannot be authorized as an ordinary rename.
func TestTemplateChangeSelection(t *testing.T) {
	resource := auth.Resource{Type: "template", ID: "template", OwnerID: "owner"}
	name := "Renamed"
	archived := "archived"
	active := "active"
	invalid := "deleted"
	for _, tc := range []struct {
		name, state *string
		action      string
	}{{&name, nil, "template.edit"}, {nil, &archived, "template.archive"}, {&name, &archived, "template.archive"}, {nil, &active, "template.edit"}, {nil, nil, ""}, {nil, &invalid, ""}} {
		target, err := web.TemplateChangeTarget(resource, tc.name, tc.state)
		if tc.action == "" {
			if err == nil {
				t.Error("invalid/empty template change accepted")
			}
			continue
		}
		if err != nil || target.Action != tc.action || target.Resource != resource {
			t.Errorf("TemplateChangeTarget = %+v, %v, want %s", target, err, tc.action)
		}
	}
}

// TestMembershipChangeSelection treats pending as access removal and validates every supplied field, including combined role/state changes.
func TestMembershipChangeSelection(t *testing.T) {
	role := "administrator"
	active := "active"
	disabled := "disabled"
	pending := "pending"
	invalid := "owner"
	for _, tc := range []struct {
		role, state *string
		action      string
	}{{&role, nil, "user.change_role"}, {nil, &active, "user.approve"}, {nil, &disabled, "user.disable"}, {nil, &pending, "user.disable"}, {&role, &pending, "user.disable"}, {&invalid, &active, ""}, {nil, nil, ""}} {
		target, err := web.MembershipChangeTarget("user", tc.role, tc.state)
		if tc.action == "" {
			if err == nil {
				t.Error("invalid membership change accepted")
			}
			continue
		}
		if err != nil || target.Action != tc.action || target.Resource.ID != "user" {
			t.Errorf("MembershipChangeTarget = %+v, %v, want %s", target, err, tc.action)
		}
	}
}
