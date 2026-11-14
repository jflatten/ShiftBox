package web_test

import (
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
	"testing"
)

// TestWorkspaceActionSelection preserves failed-operation authority and rejects retries from another workspace or unfinished operation.
func TestWorkspaceActionSelection(t *testing.T) {
	principal := auth.Principal{User: auth.User{ID: "owner"}}
	workspace := auth.Resource{Type: "workspace", ID: "workspace", OwnerID: "owner", Eligible: true}
	context := web.WorkspaceAuthorization{Workspace: workspace, LaunchTemplate: auth.Resource{Type: "template", ID: "template", State: "active"}}
	for _, action := range []string{"start", "stop", "rebuild", "repair", "delete", "update"} {
		target, err := context.Action(principal, action)
		if err != nil || target.Action != "workspace."+action || target.Resource != workspace {
			t.Errorf("Action(%q) = %+v, %v, want exact workspace action", action, target, err)
		}
	}
	if _, err := context.Action(principal, "retry"); err == nil {
		t.Error("retry without persisted operation accepted")
	}
	failed := operation.Operation{Type: "delete", Status: "failed", WorkspaceID: &workspace.ID}
	context.FailedOperation = &failed
	target, err := context.Action(principal, "retry")
	if err != nil || target.Action != "workspace.delete" {
		t.Fatalf("retry delete = %+v, %v, want workspace.delete", target, err)
	}
	failed.Status = "running"
	if _, err := context.Action(principal, "retry"); err == nil {
		t.Error("running operation accepted as failed retry")
	}
	failed.Status = "failed"
	other := "other"
	failed.WorkspaceID = &other
	if _, err := context.Action(principal, "retry"); err == nil {
		t.Error("cross-workspace retry accepted")
	}
	failed.WorkspaceID = &workspace.ID
	failed.Type = "provision"
	target, err = context.Action(principal, "retry")
	if err != nil || target.Action != "workspace.launch" || target.Resource.Type != "template" {
		t.Errorf("retry provision = %+v, %v, want template launch permission", target, err)
	}
	if _, err := context.Action(auth.Principal{User: auth.User{ID: "other", Role: "member"}}, "retry"); err == nil {
		t.Error("shared-template member could retry another owner's provisioning")
	}
	failed.Type = "backup"
	if _, err := context.Action(principal, "retry"); err == nil {
		t.Error("installation operation accepted as workspace retry")
	}
	if _, err := context.Action(principal, "connect_admin"); err == nil {
		t.Error("undeclared workspace action accepted")
	}
}

// TestWorkspaceEntrySelection chooses ownership from controller state and retains exact grants for policy and audit evaluation.
func TestWorkspaceEntrySelection(t *testing.T) {
	context := web.WorkspaceAuthorization{Workspace: auth.Resource{Type: "workspace", ID: "workspace", OwnerID: "owner", Eligible: true}, Grants: []auth.ResourceGrant{{ID: "grant", SubjectUserID: "admin", ResourceType: "workspace", ResourceID: "workspace", Action: "workspace.connect_admin"}}}
	for _, actor := range []string{"owner", "admin", "other"} {
		target, err := context.Entry(auth.Principal{User: auth.User{ID: actor}})
		want := "workspace.connect_admin"
		if actor == "owner" {
			want = "workspace.connect_owner"
		}
		if err != nil || target.Action != want || target.Resource.OwnerID != "owner" {
			t.Errorf("Entry(%q) = %+v, %v, want %s with owner context", actor, target, err, want)
		}
	}
	context.Workspace.Type = "template"
	if _, err := context.Entry(auth.Principal{User: auth.User{ID: "owner"}}); err == nil {
		t.Error("entry for non-workspace accepted")
	}
}
