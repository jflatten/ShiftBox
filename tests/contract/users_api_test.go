package contract_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestDisableMembershipCommitsStops verifies that access and durable stop
// requests are committed before tracked browser and guest connections close.
func TestDisableMembershipCommitsStops(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "offboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	now := time.Now().UTC()
	actorToken := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), actorToken, now)
	target, workspaceID := uuid.NewString(), uuid.NewString()
	session, _, err := auth.NewSession(uuid.NewString(), target, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET role='administrator' WHERE id='member'")
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'target@example.test','member','active','local','owner','now','now',1)`, target)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return auth.NewStore(db).SaveSession(ctx, tx, session) }); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
		generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		SELECT $1,id,$2,'template',1,'artifact','https://github.com/example/repo','devcontainer.json',1,512,40,'{"mode":"shared"}',
		'running','running',1,'owned','machine','now','now',1 FROM installations`, workspaceID, target)
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	pending, err := journal.Create(ctx, operation.Request{Actor: target, Key: uuid.NewString(), Type: "start", OriginatingAction: "workspace.start",
		WorkspaceID: &workspaceID, Generation: 1, Steps: []operation.Step{{Key: "start", Label: "Start workspace"}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	closed := 0
	for _, scope := range []proxy.Scope{{UserID: target, SessionID: session.ID}, {UserID: "owner", WorkspaceID: workspaceID}} {
		_, err := registry.Register(scope, func() error { return nil }, func() error {
			var desired, observed string
			var stops, revoked int
			err := db.QueryRowContext(ctx, `SELECT desired_state,observed_state,
				(SELECT count(*) FROM operations WHERE workspace_id=$1 AND originating_action='user.disable' AND status='pending'),
				(SELECT count(*) FROM sessions WHERE id=$2 AND revoked_at IS NOT NULL) FROM workspaces WHERE id=$1`,
				workspaceID, session.ID).Scan(&desired, &observed, &stops, &revoked)
			if err != nil || desired != "stopped" || observed != "running" || stops != 1 || revoked != 1 {
				t.Errorf("closure sees desired=%s observed=%s stops=%d revoked=%d (%v), want stopped/running/1/1", desired, observed, stops, revoked, err)
			}
			closed++
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	handler := web.UpdateMembershipHandler(db, origin, func() time.Time { return now }, registry, journal)
	req := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/admin/users/"+target, strings.NewReader(`{"membershipState":"disabled"}`))
	req.SetPathValue("userId", target)
	req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: actorToken})
	req.Header.Set("Origin", origin)
	req.Header.Set("If-Match", `"1"`)
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(actorToken))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 200 || closed != 2 {
		t.Fatalf("disable = %d %s, closed %d, want 200 and two closures", res.Code, res.Body, closed)
	}
	var report struct {
		StopOperations []struct {
			WorkspaceID string
			OperationID string
		}
	}
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil || len(report.StopOperations) != 1 ||
		report.StopOperations[0].WorkspaceID != workspaceID || report.StopOperations[0].OperationID == "" {
		t.Fatalf("disable stop operations = %+v (%v), want one workspace stop reference", report, err)
	}
	prior, err := journal.Get(ctx, pending.ID)
	if err != nil || prior.Status != "cancelled" {
		t.Errorf("superseded start = %s (%v), want cancelled", prior.Status, err)
	}
}

// TestApprovePendingMember requires an explicit role and a fresh membership
// version, and records the approval without exposing identity credentials.
func TestApprovePendingMember(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "approval.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	const origin = "https://management.test"
	id := uuid.NewString()
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'pending@example.test','member','pending','external','owner','now','now',1)`, id); err != nil {
		t.Fatal(err)
	}
	handler := web.UpdateMembershipHandler(db, origin, func() time.Time { return now }, &proxy.Registry{}, nil)
	send := func(body, version, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/admin/users/"+id, strings.NewReader(body))
		request.SetPathValue("userId", id)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("If-Match", version)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("PATCH membership = %d %s, want %d", response.Code, response.Body, want)
		}
		return response
	}
	csrf := auth.CSRFToken(token)
	const approval = `{"membershipState":"active","role":"administrator"}`
	send(approval, `"1"`, csrf, 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	send(approval, `"1"`, "invalid", 403)
	send(`{"membershipState":"active"}`, `"1"`, csrf, 400)
	send(approval, `"9"`, csrf, 412)
	ok := send(approval, `"1"`, csrf, 200)
	var member auth.User
	if err := json.Unmarshal(ok.Body.Bytes(), &member); err != nil || member.ID != id || member.Version != 2 ||
		member.MembershipState != "active" || member.Role != "administrator" || member.CredentialMode != "external" ||
		ok.Header().Get("ETag") != `"2"` {
		t.Fatalf("approval = %+v (%v), want active external administrator at version 2", member, err)
	}
	send(approval, `"1"`, csrf, 412)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action='user.approve' AND actor_id='member' AND resource_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("approval audit count = %d (%v), want 1", count, err)
	}
}

// TestDemoteAdministrator verifies the last-administrator guard and closes
// the former administrator's open streams after a committed demotion.
func TestDemoteAdministrator(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "roles.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	id := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'second@example.test','administrator','active','local','owner','now','now',1)`, id); err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	closed := false
	unregister, err := registry.Register(proxy.Scope{UserID: id}, func() error { return nil }, func() error {
		var role string
		err := db.QueryRowContext(ctx, "SELECT role FROM users WHERE id=$1", id).Scan(&role)
		closed = err == nil && role == "member"
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	const origin = "https://management.test"
	handler := web.UpdateMembershipHandler(db, origin, func() time.Time { return now }, registry, nil)
	demote := func(want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/admin/users/"+id, strings.NewReader(`{"role":"member"}`))
		request.SetPathValue("userId", id)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		request.Header.Set("If-Match", `"1"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("PATCH role = %d %s, want %d", response.Code, response.Body, want)
		}
	}
	demote(403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET must_change_password=1 WHERE id='owner'"); err != nil {
		t.Fatal(err)
	}
	self, selfToken, err := auth.NewSession("second-session", id, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return auth.NewStore(db).SaveSession(ctx, tx, self) }); err != nil {
		t.Fatal(err)
	}
	memberToken := token
	token = selfToken
	demote(409)
	if closed {
		t.Fatal("rejected demotion closed a connection")
	}
	token = memberToken
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	demote(200)
	if !closed {
		t.Fatal("demotion did not close the affected user's connections after commit")
	}
	demote(412)
}

// TestListUsersShowsCurrentMembership checks that the People view shows
// pending and disabled users only to a currently authorized administrator.
func TestListUsersShowsCurrentMembership(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "membership.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	for _, statement := range []string{
		"UPDATE users SET membership_state='disabled' WHERE id='owner'",
		`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('pending','pending@example.test','member','pending','external','owner','now','now',1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	const origin = "https://management.test"
	handler := web.ListUsersHandler(db, origin, func() time.Time { return now })
	read := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, origin+"/api/v1/admin/users", nil)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("GET users = %d %s, want %d", response.Code, response.Body, want)
		}
		return response
	}
	read(http.StatusForbidden)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	result := read(http.StatusOK)
	var users []auth.User
	if err := json.Unmarshal(result.Body.Bytes(), &users); err != nil || len(users) != 3 ||
		users[0].ID != "member" || users[1].MembershipState != "disabled" ||
		users[2].MembershipState != "pending" || users[2].CredentialMode != "external" {
		t.Fatalf("GET users = %+v (%v), want active, disabled and pending members ordered by email", users, err)
	}
	if strings.Contains(result.Body.String(), "externalAuthorizedAt") || strings.Contains(result.Body.String(), "tokenDigest") {
		t.Fatal("GET users exposed private identity data")
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='member' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	read(http.StatusForbidden)
}
