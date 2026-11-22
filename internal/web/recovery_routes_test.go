package web

import (
	"context"
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
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestRecoveryRoutesRequireRecoverySession verifies that review routes accept
// only recovery administrators and gate activation on completed review.
func TestRecoveryRoutesRequireRecoverySession(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "db.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	id, org, actor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	j := operation.NewJournal(db, time.Now, uuid.NewString)
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'recovery','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','v1','v1','now','now',1)`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin@example.test','administrator','active','local','setup','now','now',1),
		('pending-member','pending@example.test','member','pending','external','setup','now','now',1)`, actor); err != nil {
		t.Fatal(err)
	}
	op, err := j.Create(ctx, operation.Request{Actor: actor, Key: uuid.NewString(), Type: "restore", Generation: 1,
		OriginatingAction: "restore.activate", Steps: []operation.Step{{Key: "restore", Label: "Restore"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO recovery_reviews(id,restore_operation_id,state,started_by,started_at,version)
		VALUES($1,$2,'identity_review',$3,'now',1)`, id, op.ID, actor); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	mode := "normal"
	lookup := func(context.Context, *sql.Tx, string) (auth.Principal, error) {
		return auth.Principal{
			User: auth.User{ID: actor, Role: "administrator", MembershipState: "active", CredentialMode: "local"},
			Session: auth.Session{Mode: mode, UserID: actor, TokenDigest: auth.TokenDigest(token),
				CSRFDigest: auth.TokenDigest(auth.CSRFToken(token)), AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)},
			InstallationState: "recovery", InstallationID: org,
		}, nil
	}
	mux := http.NewServeMux()
	for _, handler := range RecoveryRoutes(db, j, "https://management.test", id, time.Now,
		func(context.Context) ([]backup.RecoveryWorkspace, error) { return nil, nil }, nil) {
		handler.Lookup = lookup
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, handler)
	}
	request := func(method, path, body, version string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "https://management.test/api/v1/recovery/"+id+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", "https://management.test")
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", "recovery-fixture-key")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s recovery = %d %s; want %d", method, w.Code, w.Body, want)
		}
		return w.Body.Bytes()
	}
	request("GET", "", "", "", 403)
	mode = "recovery_review"
	var view struct {
		Sections struct {
			Identities []struct {
				PrimaryEmail, Role, MembershipState string
			}
		}
	}
	if err := json.Unmarshal(request("GET", "", "", "", 200), &view); err != nil {
		t.Fatal(err)
	}
	identities := view.Sections.Identities
	if len(identities) != 2 || identities[0].PrimaryEmail != "admin@example.test" ||
		identities[1].PrimaryEmail != "pending@example.test" || identities[1].Role != "member" || identities[1].MembershipState != "pending" {
		t.Fatal("authenticated recovery review must show both restored identities and their current membership")
	}
	request("POST", "/activate", "", "", 403)
	request("PATCH", "", `{"section":"identities","decisions":{"userIds":["`+actor+`","pending-member"]}}`, `"1"`, 200)
	request("PATCH", "", `{"section":"provider","decisions":{}}`, `"2"`, 200)
	request("PATCH", "", `{"section":"authorization_retention","decisions":{"acceptRetention":true}}`, `"3"`, 200)
	request("POST", "/activate", "", "", 202)
}
