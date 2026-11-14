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
