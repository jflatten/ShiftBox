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
	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestAuditList checks bounded history, stable pagination under concurrent
// additions, exact filters, and the current administrator permission.
func TestAuditList(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	token := strings.Repeat("A", 43)
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, uuid.NewString(), token, now)
	first, second := uuid.NewString(), uuid.NewString()
	appendEvents := func(count int, action, resource string) {
		t.Helper()
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			for range count {
				if _, err := audit.Append(ctx, tx, audit.Event{
					OccurredAt: now, ActorType: "user", ActorID: "owner", Action: action,
					ResourceType: "user", ResourceID: resource, Outcome: "allowed", ReasonCode: "membership_approved",
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendEvents(51, "user.approve", first)
	appendEvents(1, "user.disable", second)
	appendEvents(1, "user.disable", first)
	appendEvents(1, "user.approve", second)
	handler := web.ListAuditEventsHandler(db, origin, func() time.Time { return now })
	read := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, origin+path, nil)
		request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("GET audit status = %d %s, want %d", response.Code, response.Body, want)
		}
		return response
	}
	const path = "/api/v1/admin/audit"
	read(path, 403)
	if _, err := db.ExecContext(ctx, "UPDATE users SET role='administrator' WHERE id='member'"); err != nil {
		t.Fatal(err)
	}
	read(path+"?cursor=-1", 400)
	type row struct{ ID, Action, ResourceID string }
	decode := func(response *httptest.ResponseRecorder, count int) []row {
		t.Helper()
		var rows []row
		if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || len(rows) != count {
			t.Fatalf("audit rows = %d entries (%v), want %d", len(rows), err, count)
		}
		for _, item := range rows {
			if _, err := uuid.Parse(item.ID); err != nil || item.Action != "user.approve" || item.ResourceID != first {
				t.Fatalf("audit row = %+v (%v), want UUID and filtered resource/action", item, err)
			}
		}
		return rows
	}
	page := read(path+"?action=user.approve&resourceId="+first, 200)
	rows := decode(page, 50)
	link := page.Header().Get("Link")
	next, ok := strings.CutSuffix(strings.TrimPrefix(link, "<"), `>; rel="next"`)
	if !ok || !strings.HasPrefix(next, path+"?") {
		t.Fatalf("audit Link = %q, want next page", link)
	}
	appendEvents(1, "user.approve", first)
	last := read(next, 200)
	lastRows := decode(last, 1)
	for _, item := range rows {
		if item.ID == lastRows[0].ID {
			t.Fatal("next page repeated an event")
		}
	}
	if last.Header().Get("Link") != "" {
		t.Fatal("last page unexpectedly advertises a next page")
	}
}
