package web_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspacessh"
)

const (
	connectionGrantOrigin    = "https://management.test"
	connectionGrantOwner     = "00000000-0000-4000-8000-000000000201"
	connectionGrantee        = "00000000-0000-4000-8000-000000000202"
	connectionGrantMember    = "00000000-0000-4000-8000-000000000203"
	connectionGrantID        = "00000000-0000-4000-8000-000000000204"
	connectionGrantWorkspace = "00000000-0000-4000-8000-000000000205"
)

// TestWorkspaceConnectionGrantListAndRevokeSSH verifies that a separately
// provisioned grant is visible to an authorized administrator and that the
// existing revoke endpoint closes the grant's live SSH connection.
func TestWorkspaceConnectionGrantListAndRevokeSSH(t *testing.T) {
	db, now := connectionGrantFixture(t)
	registry := &proxy.Registry{}
	clock := func() time.Time { return now }
	admin := connectionGrantSession(t, db, now, connectionGrantOwner, "admin-browser")
	member := connectionGrantSession(t, db, now, connectionGrantMember, "member-browser")
	list, revoke := web.WorkspaceConnectionGrantHandlers(db, connectionGrantOrigin, clock, registry)
	page := connectionGrantRequest(&list, admin, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), connectionGrantID) ||
		!strings.Contains(page.Body.String(), "ssh-grantee@example.test") {
		t.Fatalf("list = %d %s, want the saved grant and grantee email", page.Code, page.Body)
	}
	if denied := connectionGrantRequest(&revoke, member, connectionGrantID); denied.Code != http.StatusForbidden {
		t.Fatalf("member revoke = %d, want 403", denied.Code)
	}
	token := issueWorkspaceSSHFixtureCredential(t, db, now, connectionGrantee, "grantee-helper")
	peer := make(chan net.Conn, 1)
	api := web.NewWorkspaceSSHAPI(db, registry, nil, func(_ context.Context, id string, generation int64, _ string) (net.Conn, string, error) {
		if id != connectionGrantWorkspace || generation != 1 {
			return nil, "", fmt.Errorf("unexpected SSH workspace %s, generation %d", id, generation)
		}
		controller, guest := net.Pipe()
		peer <- guest
		return controller, "fixture-key", nil
	}, clock, connectionGrantOrigin, nil)
	server := httptest.NewTLSServer(api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connect := func() (*websocket.Conn, *http.Response, error) {
		return websocket.Dial(ctx, server.URL+workspacessh.ConnectPath(connectionGrantWorkspace), &websocket.DialOptions{
			HTTPClient: server.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
			Subprotocols: []string{workspacessh.Subprotocol},
		})
	}
	socket, _, err := connect()
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	var ready workspacessh.Control
	if err := wsjson.Read(ctx, socket, &ready); err != nil || ready.Type != workspacessh.Ready {
		t.Fatalf("granted SSH stream readiness = %+v, %v, want ready", ready, err)
	}
	guest := <-peer
	defer guest.Close()
	unrelatedClosed := false
	keep, err := registry.Register(proxy.Scope{WorkspaceID: connectionGrantWorkspace, GrantID: "another-grant", Service: "ssh"},
		func() error { return nil }, func() error { unrelatedClosed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer keep()
	res := connectionGrantRequest(&revoke, admin, connectionGrantID)
	if res.Code != http.StatusNoContent || unrelatedClosed {
		t.Fatalf("revoke = %d and unrelated closed %t, want 204 and unrelated open", res.Code, unrelatedClosed)
	}
	if _, _, err := socket.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("SSH relay after revoke = %v, context %v, want immediate disconnect", err, ctx.Err())
	}
	if err := guest.SetReadDeadline(time.Now().Add(time.Second)); err != nil && err != io.ErrClosedPipe {
		t.Fatal(err)
	}
	if _, err := guest.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("matching SSH relay read = %v, want EOF", err)
	}
	if retry, response, err := connect(); err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		if retry != nil {
			retry.CloseNow()
		}
		t.Fatalf("reconnect with revoked grant = %v, %v, want HTTP 403", response, err)
	}
	page = connectionGrantRequest(&list, admin, "")
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), connectionGrantID) {
		t.Errorf("list after revoke = %d %s, want no active grant", page.Code, page.Body)
	}
}

func connectionGrantFixture(t *testing.T) (*sql.DB, time.Time) {
	t.Helper()
	db, now := readySSHWorkspace(t)
	executeLifecycleFixture(t, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin-owner@example.test','administrator','active','local','setup','now','now',1),
		($2,'ssh-grantee@example.test','administrator','active','local','setup','now','now',1),
		($3,'member@example.test','member','active','local','setup','now','now',1)`,
		connectionGrantOwner, connectionGrantee, connectionGrantMember)
	executeLifecycleFixture(t, db, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
		cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version)
		SELECT $1,installation_id,$2,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
		cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,'running','running',1,'owned-grant-fixture',created_at,updated_at,version
		FROM workspaces WHERE id=$3`, connectionGrantWorkspace, connectionGrantOwner, lifecycleWorkspaceID)
	executeLifecycleFixture(t, db, `INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
		VALUES('grant-ssh-start',$1,'start',1,$2,'grant-ssh-start','succeeded','2026-11-13T23:00:00Z',1,'workspace.start');
		INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
		VALUES('grant-ssh-start',0,'verify_ide','Verify IDE','succeeded')`, connectionGrantWorkspace, connectionGrantOwner)
	executeLifecycleFixture(t, db, `INSERT INTO resource_grants(id,resource_type,resource_id,subject_user_id,action,granted_by,granted_at)
		VALUES($1,'workspace',$2,$3,'workspace.connect_admin',$4,$5)`,
		connectionGrantID, connectionGrantWorkspace, connectionGrantee, connectionGrantOwner, now.Format(time.RFC3339))
	return db, now
}

func connectionGrantSession(t *testing.T, db *sql.DB, now time.Time, user, id string) string {
	t.Helper()
	session, token, err := auth.NewSession(id, user, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func connectionGrantRequest(handler *web.AuthorizedHandler, token, grantID string) *httptest.ResponseRecorder {
	path := connectionGrantOrigin + "/api/v1/admin/workspaces/" + connectionGrantWorkspace + "/connection-grants"
	if grantID != "" {
		path += "/" + grantID
	}
	r := httptest.NewRequest(handler.Route.Method, path, nil)
	r.SetPathValue("workspaceId", connectionGrantWorkspace)
	r.SetPathValue("grantId", grantID)
	r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	r.Header.Set("Origin", connectionGrantOrigin)
	r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	r.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, r)
	return res
}
