package web_test

import (
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
	"testing"
)

// TestOperationViewAuthority rejects mismatched workspace context and missing or weakened installation authority.
func TestOperationViewAuthority(t *testing.T) {
	id := "workspace"
	op := operation.Operation{WorkspaceID: &id, OriginatingAction: "workspace.delete"}
	resource := auth.Resource{Type: "workspace", ID: id, OwnerID: "owner"}
	target, err := web.OperationViewTarget(op, resource)
	if err != nil || target.Action != "workspace.view" || target.Resource != resource {
		t.Fatalf("workspace operation view = %+v, %v", target, err)
	}
	resource.ID = "other"
	if _, err := web.OperationViewTarget(op, resource); err == nil {
		t.Error("cross-workspace operation view accepted")
	}
	op.WorkspaceID = nil
	op.OriginatingAction = "backup.create"
	target, err = web.OperationViewTarget(op, auth.Resource{Type: "installation", ID: "installation"})
	if err != nil || target.Action != "backup.create" {
		t.Fatalf("backup view = %+v, %v", target, err)
	}
	for _, action := range []string{"", "session.view", "workspace.view", "template.view"} {
		op.OriginatingAction = action
		if _, err := web.OperationViewTarget(op, auth.Resource{Type: "installation", ID: "installation"}); err == nil {
			t.Errorf("installation view accepted %q", action)
		}
	}
}
