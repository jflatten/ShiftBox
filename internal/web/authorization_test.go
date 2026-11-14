package web_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestAuthorizedHandlerCommitsBeforeResponse proves denied and failed requests cannot commit intent or emit success.
func TestAuthorizedHandlerCommitsBeforeResponse(t *testing.T) {
	session, principal, token := sessionHandlerFixture(t)
	db, err := database.Open(context.Background(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "authorization.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE intents (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	fail := false
	handler := web.AuthorizedHandler{
		DB: db, Origin: "https://management.test", Now: session.Now,
		Route:  web.ManagementRoute{Method: "POST", Action: "template.create"},
		Lookup: func(ctx context.Context, tx *sql.Tx, token string) (auth.Principal, error) { return *principal, nil },
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "template.create"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, target web.AuthorizationTarget, decision auth.Decision) (http.Handler, error) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO intents(id) VALUES(1)`); err != nil {
				return nil, err
			}
			if fail {
				return nil, errors.New("private storage failure")
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM intents`).Scan(&count); err != nil || count != 1 {
					t.Errorf("committed intents = %d, error %v, want 1", count, err)
				}
				w.WriteHeader(204)
			}), nil
		},
	}
	request := func(origin string) int {
		r := httptest.NewRequest("POST", "https://management.test/api/v1/templates", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("https://guest.preview.test"); got != 403 {
		t.Fatalf("hostile origin status = %d, want 403", got)
	}
	principal.User.MembershipState = "disabled"
	if got := request(handler.Origin); got != 401 {
		t.Fatalf("disabled membership status = %d, want 401", got)
	}
	principal.User.MembershipState = "active"
	handler.Route.Action = "user.create"
	if got := request(handler.Origin); got != 500 {
		t.Fatalf("resolver/action mismatch status = %d, want 500", got)
	}
	handler.Route.Action = "template.create"
	fail = true
	if got := request(handler.Origin); got != 500 {
		t.Fatalf("failed intent status = %d, want 500", got)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM intents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed requests committed %d intents, want 0", count)
	}
	fail = false
	if got := request(handler.Origin); got != 204 {
		t.Fatalf("authorized request status = %d, want 204", got)
	}
	// Administrator connections must fail closed when auditing is absent or
	// fails, and an audit must roll back if later intent creation fails.
	if _, err := db.Exec(`DELETE FROM intents; CREATE TABLE audits (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	principal.User.Role = "administrator"
	handler.Route.Action = "workspace.connect_admin"
	handler.Resolve = func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal) (web.AuthorizationTarget, error) {
		return web.AuthorizationTarget{Action: "workspace.connect_admin", Resource: auth.Resource{Type: "workspace", ID: "workspace", OwnerID: "another-user", Eligible: true}, Grants: []auth.ResourceGrant{{ID: "grant", SubjectUserID: p.User.ID, ResourceType: "workspace", ResourceID: "workspace", Action: "workspace.connect_admin"}}}, nil
	}
	if got := request(handler.Origin); got != 500 {
		t.Fatalf("missing audit status = %d, want 500", got)
	}
	handler.Audit = func(ctx context.Context, tx *sql.Tx, p auth.Principal, target web.AuthorizationTarget) error {
		return errors.New("audit storage unavailable")
	}
	if got := request(handler.Origin); got != 500 {
		t.Fatalf("failed audit status = %d, want 500", got)
	}
	handler.Audit = func(ctx context.Context, tx *sql.Tx, p auth.Principal, target web.AuthorizationTarget) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO audits(id) VALUES(1)`)
		return err
	}
	fail = true
	if got := request(handler.Origin); got != 500 {
		t.Fatalf("failed audited intent status = %d, want 500", got)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audits`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back audits = %d, want 0", count)
	}
	fail = false
	if got := request(handler.Origin); got != 204 {
		t.Fatalf("audited connection status = %d, want 204", got)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audits`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed audits = %d, want 1", count)
	}

	// The default writer persists the exact owner/grant evidence in the real
	// audit schema before an administrator connection response is released.
	if err := database.Migrate(context.Background(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM intents`); err != nil {
		t.Fatal(err)
	}
	handler.Audit = nil
	if got := request(handler.Origin); got != 204 {
		t.Fatalf("default durable audit status = %d, want 204", got)
	}
	var metadata string
	if err := db.QueryRow(`SELECT metadata_json FROM audit_events`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata != `{"ownerId":"another-user","grantId":"grant"}` {
		t.Errorf("access audit metadata = %s, want owner and exact grant", metadata)
	}

}
