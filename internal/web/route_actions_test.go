package web_test

import (
	"testing"

	"github.com/jflatten/ShiftBox/internal/web"
)

// TestContextualRouteActions prevents a resolver from substituting an easier permission outside its reviewed route contract.
func TestContextualRouteActions(t *testing.T) {
	allowed := map[string][]string{
		"workspace_action":   {"workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair", "workspace.delete", "workspace.update", "workspace.launch", "workspace.connect_owner", "workspace.connect_admin"},
		"workspace_entry":    {"workspace.connect_owner", "workspace.connect_admin"},
		"operation_resource": {"workspace.view", "template.view", "setup.mutate", "infrastructure.manage", "maintenance.manage", "backup.create", "restore.validate", "restore.activate", "recovery.activate", "migration.preflight"},
		"template_update":    {"template.edit", "template.archive"},
		"secret_scope":       {"secret.manage_user", "secret.manage_template", "secret.manage_organization"},
		"membership_update":  {"user.approve", "user.disable", "user.change_role"},
		"notice_receipt":     {"notice.read", "notice.dismiss"},
		// The browser return delegates all state changes to the protected POST.
		"github_app_browser_return": {},
	}
	for _, route := range web.ManagementRoutes() {
		actions, reviewed := allowed[route.Resolver]
		if route.Action != "" {
			actions = []string{route.Action}
			reviewed = true
		}
		if !reviewed {
			t.Fatalf("route %s has no tested action set", route.OperationID)
		}
		for _, action := range actions {
			if !route.AcceptsAction(action) {
				t.Errorf("%s rejects reviewed action %s", route.OperationID, action)
			}
		}
		for _, action := range []string{"", "invented.permission", "session.view"} {
			if action == route.Action {
				continue
			}
			if route.AcceptsAction(action) {
				t.Errorf("%s accepts unrelated action %q", route.OperationID, action)
			}
		}
		if route.Resolver == "github_app_browser_return" && route.AcceptsAction("infrastructure.manage") {
			t.Error("the browser return accepted a management mutation")
		}
	}
	if (web.ManagementRoute{Resolver: "unknown"}).AcceptsAction("template.create") {
		t.Error("unknown resolver accepted action")
	}
}
