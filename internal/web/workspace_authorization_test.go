package web_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
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

const lifecycleWorkspaceID = "00000000-0000-4000-8000-000000000035"

type lifecycleRead struct {
	Current  string  `json:"current"`
	Next     *string `json:"next"`
	Mode     string  `json:"mode"`
	Workflow *string `json:"workflowOperationId"`
}

type lifecycleWorkspaceRead struct {
	ID         string        `json:"id"`
	Lifecycle  lifecycleRead `json:"lifecycle"`
	CanOpenIDE bool          `json:"canOpenIde"`
}

type lifecyclePage struct {
	Items      []lifecycleWorkspaceRead `json:"items"`
	NextCursor *string                  `json:"nextCursor"`
}

// TestWorkspaceLifecycleReads verifies shared, durable display evidence, including
// readiness before all hooks finish, pre-existing records, and workflow handoffs.
func TestWorkspaceLifecycleReads(t *testing.T) {
	type step struct{ key, status string }
	type workflow struct {
		kind, action, status string
		steps                []step
	}
	done := func(key string) step { return step{key, "succeeded"} }
	pending := func(key string) step { return step{key, "pending"} }
	ready := workflow{"start", "workspace.start", "succeeded", []step{done("start"), done("configure"), done("verify_ide")}}
	cases := []struct {
		name, desired, observed string
		workflows               []workflow
		current, next, mode     string
	}{
		{"previous-generation readiness ignored", "running", "running", nil, "vm-running", "", "settled"},
		{"unreachable", "running", "unreachable", nil, "unreachable", "", "settled"},
		{"launch owns no confirmed resource", "running", "provisioning", []workflow{
			{"provision", "workspace.launch", "pending", []step{pending("provision"), pending("enroll")}},
		}, "unknown", "provisioned", "transitioning"},
		{"provisioned before VM enrollment", "running", "starting", []workflow{
			{"provision", "workspace.launch", "waiting", []step{done("provision"), pending("enroll"), pending("configure"), pending("verify_ide")}},
		}, "provisioned", "vm-running", "transitioning"},
		{"start waits for provider", "running", "stopped", []workflow{
			{"start", "workspace.start", "waiting", []step{pending("start"), pending("configure")}},
		}, "stopped", "vm-running", "transitioning"},
		{"scheduled maintenance waits behind startup", "running", "running", []workflow{
			{"start", "workspace.start", "waiting", []step{done("start"), pending("configure"), pending("verify_ide")}},
		}, "vm-running", "container-ready", "transitioning"},
		{"configured but IDE unverified", "running", "running", []workflow{
			{"start", "workspace.start", "waiting", []step{done("start"), done("configure"), pending("verify_ide")}},
		}, "container-ready", "ready", "transitioning"},
		{"IDE verified during hooks", "running", "running", []workflow{
			{"start", "workspace.start", "waiting", []step{done("start"), done("configure"), done("verify_ide"), pending("finish_setup")}},
		}, "ready", "", "settled"},
		{"hook fails after readiness", "running", "running", []workflow{
			{"start", "workspace.start", "failed", []step{done("start"), done("configure"), done("verify_ide"), {"finish_setup", "failed"}}},
		}, "ready", "", "settled"},
		{"IDE failure", "running", "running", []workflow{
			{"start", "workspace.start", "failed", []step{done("start"), done("configure"), {"verify_ide", "failed"}}},
		}, "container-ready", "failed", "failed"},
		{"rebuild", "running", "running", []workflow{ready,
			{"rebuild", "workspace.rebuild", "running", []step{pending("configure"), pending("verify_ide")}},
		}, "ready", "container-ready", "transitioning"},
		{"verified repair", "running", "running", []workflow{
			{"repair", "workspace.repair", "succeeded", []step{done("repair_enter"), done("verify_repair")}},
		}, "repair-ready", "", "settled"},
		{"retry from repair", "running", "running", []workflow{
			{"repair", "workspace.repair", "succeeded", []step{done("repair_enter"), done("verify_repair")}},
			{"rebuild", "workspace.repair", "waiting", []step{pending("configure"), pending("verify_ide")}},
		}, "repair-ready", "container-ready", "transitioning"},
		{"manual stop after verified start", "stopped", "running", []workflow{ready,
			{"stop", "workspace.stop", "running", []step{done("shutdown"), pending("stop")}},
		}, "ready", "stopped", "transitioning"},
		{"inactivity stop verified", "stopped", "stopped", []workflow{ready,
			{"stop", "workspace.inactivity_stop", "succeeded", []step{done("stop")}},
		}, "stopped", "", "settled"},
		{"offboarding replaces startup", "stopped", "running", []workflow{
			{"start", "workspace.start", "cancelled", []step{done("start"), pending("configure")}},
			{"stop", "user.disable", "pending", []step{pending("stop")}},
		}, "vm-running", "stopped", "transitioning"},
		{"maintenance staging on running guest", "running", "running", []workflow{ready,
			{"update", "maintenance.update_guest", "waiting", []step{done("maintenance_stage"), pending("maintenance_activate"), pending("maintenance_verify")}},
		}, "ready", "updated", "transitioning"},
		{"management boot never Ready", "stopped", "running", []workflow{
			{"update", "maintenance.update_guest", "waiting", []step{done("maintenance_boot"), done("maintenance_verify"), pending("maintenance_restore")}},
		}, "updated", "stopped", "transitioning"},
		{"manual delete from stopped", "deleted", "stopped", []workflow{
			{"delete", "workspace.delete", "pending", []step{pending("shutdown"), pending("stop"), pending("delete")}},
		}, "stopped", "deleted", "transitioning"},
		{"retention delete verified provider cleanup", "deleted", "deleted", []workflow{
			{"delete", "workspace.retention_delete", "succeeded", []step{done("shutdown"), done("stop"), done("delete")}},
		}, "deleted", "", "settled"},
	}

	db := keepAliveDatabase(t)
	principal, request := lifecycleReads(t, db)
	principal.User.Role = "administrator"
	principal.User.ID, principal.Session.UserID = "administrator", "administrator"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executeLifecycleFixture(t, db, `DELETE FROM maintenance_targets; DELETE FROM maintenance_campaigns; DELETE FROM operation_steps; DELETE FROM operations;
				UPDATE workspaces SET desired_state=$1,observed_state=$2,provider_instance_id='instance'`, tc.desired, tc.observed)
			executeLifecycleFixture(t, db, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
				VALUES('older-generation',$1,'start',2,'user','older-generation','succeeded','2026-11-13T20:00:00Z',1,'workspace.start');
				INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
				VALUES('older-generation',0,'verify_ide','Verify IDE','succeeded')`, lifecycleWorkspaceID)
			var workflowID *string
			for i, work := range tc.workflows {
				id := []string{"first", "second"}[i]
				executeLifecycleFixture(t, db, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
					VALUES($1,$2,$3,1,'user',$1,$4,$5,1,$6)`, id, lifecycleWorkspaceID, work.kind, work.status,
					[]string{"2026-11-13T22:00:00Z", "2026-11-13T23:00:00Z"}[i], work.action)
				for index, phase := range work.steps {
					executeLifecycleFixture(t, db, `INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
						VALUES($1,$2,$3,$3,$4)`, id, index, phase.key, phase.status)
				}
				workflowID = &id
			}
			if tc.name == "scheduled maintenance waits behind startup" {
				executeLifecycleFixture(t, db, `INSERT INTO operations(id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
					VALUES('campaign','update',1,'user','campaign','waiting','2026-11-13T23:30:00Z',1,'maintenance.schedule');
					INSERT INTO maintenance_campaigns(operation_id,installation_id,release_id,deadline,created_by,created_at,state)
					SELECT 'campaign',installation_id,'next','2026-11-13T23:45:00.000000000Z','user','2026-11-13T23:30:00Z','active' FROM workspaces;
					INSERT INTO maintenance_targets(campaign_id,workspace_id,generation,prior_desired_state,prior_observed_state,state,updated_at)
					VALUES('campaign',$1,1,'running','running','waiting_safe','2026-11-13T23:30:00Z')`, lifecycleWorkspaceID)
			}
			want := lifecycleRead{Current: tc.current, Mode: tc.mode, Workflow: workflowID}
			if tc.next != "" {
				want.Next = &tc.next
			}
			var detail lifecycleWorkspaceRead
			decodeLifecycleRead(t, request("/"+lifecycleWorkspaceID), &detail)
			var page lifecyclePage
			decodeLifecycleRead(t, request("?lifecycleState="+tc.current), &page)
			if !reflect.DeepEqual(detail.Lifecycle, want) || detail.CanOpenIDE {
				t.Errorf("GET detail %s = %+v (IDE access %t), want %+v with no admin grant", tc.name, detail.Lifecycle, detail.CanOpenIDE, want)
			}
			if len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Lifecycle, detail.Lifecycle) {
				t.Errorf("GET list with current chip %q = %+v, want detail lifecycle %+v", tc.current, page, detail.Lifecycle)
			}
		})
	}
}

// TestWorkspaceLifecycleFilterAppliesBeforePagination preserves inventory scope
// even when matching rows sort after more than one page of other states.
func TestWorkspaceLifecycleFilterAppliesBeforePagination(t *testing.T) {
	db := keepAliveDatabase(t)
	const otherOwner = "00000000-0000-4000-8000-000000000093"
	const unreachable = "00000000-0000-4000-8000-000000000151"
	executeLifecycleFixture(t, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('00000000-0000-4000-8000-000000000093','other@example.test','member','active','local','setup','now','now',1);
		WITH RECURSIVE offsets(n) AS (SELECT 100 UNION ALL SELECT n+1 FROM offsets WHERE n<152)
		INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
			cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version)
		SELECT printf('00000000-0000-4000-8000-%012d',n),installation_id,
			CASE WHEN n=152 THEN '00000000-0000-4000-8000-000000000093' ELSE owner_user_id END,
			template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
			CASE WHEN n=150 THEN 'deleted' ELSE 'stopped' END,
			CASE WHEN n=150 THEN 'deleted' WHEN n>=151 THEN 'unreachable' ELSE 'stopped' END,
			generation,printf('fixture-%d',n),created_at,updated_at,version FROM workspaces CROSS JOIN offsets`)
	principal, request := lifecycleReads(t, db)
	var page lifecyclePage
	decodeLifecycleRead(t, request("?lifecycleState=unreachable"), &page)
	if len(page.Items) != 1 || page.Items[0].ID != unreachable || page.NextCursor != nil {
		t.Fatalf("lifecycle-filtered member page = %+v, want only %s with no cursor", page, unreachable)
	}
	decodeLifecycleRead(t, request("?lifecycleState=deleted&excludeDeleted=true"), &page)
	if len(page.Items) != 0 {
		t.Errorf("hidden deleted page = %+v, want no items", page)
	}
	if response := request("?lifecycleState=failed"); response.Code != http.StatusBadRequest {
		t.Errorf("GET list with right-side failure filter = %d, want 400", response.Code)
	}
	principal.User.Role = "administrator"
	decodeLifecycleRead(t, request("?owner="+otherOwner+"&state=unreachable&lifecycleState=unreachable"), &page)
	if len(page.Items) != 1 || page.Items[0].ID == unreachable {
		t.Errorf("scoped administrator page = %+v, want only the other owner's workspace", page)
	}
}

func executeLifecycleFixture(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func lifecycleReads(t *testing.T, db *sql.DB) (*auth.Principal, func(string) *httptest.ResponseRecorder) {
	t.Helper()
	session, principal, token := sessionHandlerFixture(t)
	list, detail := web.WorkspaceReadHandlers(db, session.Origin, session.Now, nil)
	lookup := func(context.Context, *sql.Tx, string) (auth.Principal, error) { return *principal, nil }
	list.Lookup, detail.Lookup = lookup, lookup
	return principal, func(suffix string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, session.Origin+"/api/v1/workspaces"+suffix, nil)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		if len(suffix) > 0 && suffix[0] == '/' {
			r.SetPathValue("workspaceId", suffix[1:])
			detail.ServeHTTP(w, r)
		} else {
			list.ServeHTTP(w, r)
		}
		return w
	}
}

func decodeLifecycleRead(t *testing.T, response *httptest.ResponseRecorder, view any) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("GET workspace = %d %s, want 200", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), view); err != nil {
		t.Fatal(err)
	}
}

// TestWorkspaceLifecycleTimeoutClearsOnLateCompletion keeps a grouped
// transition failed until its final milestone, even after an intermediate step succeeds.
func TestWorkspaceLifecycleTimeoutClearsOnLateCompletion(t *testing.T) {
	db := keepAliveDatabase(t)
	_, request := lifecycleReads(t, db)
	executeLifecycleFixture(t, db, `UPDATE workspaces SET desired_state='stopped',observed_state='running';
		INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
		VALUES('stop',$1,'stop',1,'user','stop','waiting','2026-11-13T23:00:00Z',1,'workspace.stop');
		INSERT INTO operation_steps(operation_id,step_index,step_key,label,status,transition_timeout_at,transition_timeout_reason)
		VALUES('stop',0,'shutdown','Graceful shutdown','succeeded','2026-11-13T23:05:00Z','Waiting for guest shutdown.'),
		      ('stop',1,'stop','Verify stop','active',NULL,NULL)`, lifecycleWorkspaceID)
	check := func(current, mode string, next *string) {
		t.Helper()
		var detail lifecycleWorkspaceRead
		decodeLifecycleRead(t, request("/"+lifecycleWorkspaceID), &detail)
		want := lifecycleRead{Current: current, Next: next, Mode: mode, Workflow: new("stop")}
		if !reflect.DeepEqual(detail.Lifecycle, want) {
			t.Errorf("stop lifecycle = %+v, want %+v", detail.Lifecycle, want)
		}
	}
	check("vm-running", "failed", new("failed"))
	executeLifecycleFixture(t, db, `UPDATE workspaces SET observed_state='stopped';
		UPDATE operation_steps SET status='succeeded' WHERE step_key='stop';
		UPDATE operations SET status='succeeded' WHERE id='stop'`)
	check("stopped", "settled", nil)
}
