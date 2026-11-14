package contract_test

import (
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
	"github.com/jflatten/ShiftBox/internal/web"
)

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
	handler := web.UpdateMembershipHandler(db, origin, func() time.Time { return now })
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
