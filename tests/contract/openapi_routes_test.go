package contract_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/web"
)

// TestManagementRouteContract catches added, removed, duplicated, or moved API routes before handler registration.
func TestManagementRouteContract(t *testing.T) {
	data, err := os.ReadFile("../../specs/001-shiftbox-mvp/contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// This intentionally follows the checked-in contract's block layout and fails
	// closed when its operation declarations no longer have that layout.
	methodLine := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	want := make(map[string]string)
	var path, method string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "  /") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			method = ""
		}
		if match := methodLine.FindStringSubmatch(line); match != nil {
			method = strings.ToUpper(match[1])
		}
		if strings.HasPrefix(line, "      operationId:") {
			id := strings.TrimSpace(strings.TrimPrefix(line, "      operationId:"))
			if path == "" || method == "" || want[id] != "" {
				t.Fatalf("OpenAPI operation %q has missing or duplicated route context", id)
			}
			want[id] = method + " " + path
		}
	}
	if len(want) == 0 {
		t.Fatal("OpenAPI operation enumeration is empty")
	}
	paths := strings.Split(string(data), "\ncomponents:")[0]
	if declared := strings.Count(paths, "operationId:"); declared != len(want) {
		t.Fatalf("OpenAPI operation enumeration found %d of %d declarations; update the contract reader for the changed layout", len(want), declared)
	}
	for _, route := range append(web.ManagementRoutes(), web.WorkspaceSSHRoutes()...) {
		if got := route.Method + " " + route.Path; got != want[route.OperationID] {
			t.Errorf("ManagementRoutes(%s) = %q, want %q", route.OperationID, got, want[route.OperationID])
		}
		if (route.Action == "") == (route.Resolver == "") {
			t.Errorf("ManagementRoutes(%s) must declare exactly one action or contextual resolver", route.OperationID)
		}
		delete(want, route.OperationID)
	}
	for id := range want {
		t.Errorf("OpenAPI operation %s has no authorization route declaration", id)
	}
}

// TestRouteActionsMatchReviewedMatrix prevents ordinary routes from silently acquiring a different permission.
func TestRouteActionsMatchReviewedMatrix(t *testing.T) {
	data, err := os.ReadFile("../../specs/001-shiftbox-mvp/contracts/authorization-matrix.md")
	if err != nil {
		t.Fatal(err)
	}
	section := strings.Split(string(data), "## Management operation mapping")[1]
	section = strings.Split(section, "## Session and installation gates")[0]
	quoted := regexp.MustCompile("`([^`]+)`")
	actions := make(map[string]string)
	for _, line := range strings.Split(section, "\n") {
		columns := strings.Split(line, "|")
		if len(columns) < 4 {
			continue
		}
		keys := quoted.FindAllStringSubmatch(columns[2], -1)
		if len(keys) != 1 || !strings.Contains(keys[0][1], ".") || strings.Contains(keys[0][1], "<") {
			continue
		}
		for _, id := range quoted.FindAllStringSubmatch(columns[1], -1) {
			actions[id[1]] = keys[0][1]
		}
	}
	// These rows group different static actions with one field-dependent operation.
	actions["listUsers"], actions["createLocalUser"] = "user.list", "user.create"
	contextual := map[string]string{
		"requestWorkspaceAction": "workspace_action", "createWorkspaceEntryGrant": "workspace_entry",
		"getOperation": "operation_resource", "streamOperationEvents": "operation_resource",
		"updateTemplate": "template_update", "listSecretMetadata": "secret_scope",
		"setSecret": "secret_scope", "deleteSecretOverride": "secret_scope",
		"updateMembership": "membership_update", "updateNoticeReceipt": "notice_receipt",
		"returnGitHubAppRegistration": "github_app_browser_return",
	}
	for _, route := range web.ManagementRoutes() {
		if resolver, ok := contextual[route.OperationID]; ok {
			if route.Resolver != resolver || route.Action != "" {
				t.Errorf("route %s = action %q/resolver %q, want contextual %q", route.OperationID, route.Action, route.Resolver, resolver)
			}
			continue
		}
		if want := actions[route.OperationID]; want == "" || route.Action != want || route.Resolver != "" {
			t.Errorf("route %s action = %q, want reviewed %q", route.OperationID, route.Action, want)
		}
	}
}

// TestWorkspaceLifecycleReadContract keeps the lifecycle filter additive and
// declares the common lifecycle snapshot in the existing workspace schema.
func TestWorkspaceLifecycleReadContract(t *testing.T) {
	data, err := os.ReadFile("../../specs/001-shiftbox-mvp/contracts/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range []string{
		"name: state, in: query", "name: lifecycleState",
		"lifecycle: { $ref: '#/components/schemas/WorkspaceLifecycle' }",
		"required: [current, next, mode, workflowOperationId]",
		"enum: [settled, transitioning, timed_out, failed]",
		"canInterruptDeployment:",
	} {
		if !strings.Contains(string(data), declaration) {
			t.Errorf("workspace read contract missing %q", declaration)
		}
	}
}
