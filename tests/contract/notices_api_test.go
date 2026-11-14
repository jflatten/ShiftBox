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
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestNoticeReceiptsArePersonal verifies publication authority and that one
// user's dismissal never removes a notice for another user.
func TestNoticeReceiptsArePersonal(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "notices.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	memberToken := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, uuid.NewString(), memberToken, now)
	session, adminToken, err := auth.NewSession(uuid.NewString(), "owner", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return auth.NewStore(db).SaveSession(ctx, tx, session) }); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	publish, list, receipt := web.NoticeHandlers(db, origin, func() time.Time { return now }, uuid.NewString)
	mux := http.NewServeMux()
	for _, handler := range []*web.AuthorizedHandler{&publish, &list, &receipt} {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, handler)
	}
	send := func(token, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+"/api/v1"+path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, response.Code, response.Body, want)
		}
		return response
	}
	const body = `{"severity":"warning","title":"Storage work","body":"Push any work you need to keep."}`
	send(memberToken, "POST", "/admin/notices", body, 403)
	published := send(adminToken, "POST", "/admin/notices", body, 201)
	type view struct {
		ID, Title           string
		ReadAt, DismissedAt *time.Time
	}
	var notice view
	if err := json.Unmarshal(published.Body.Bytes(), &notice); err != nil || !web.ValidUUID(notice.ID) {
		t.Fatalf("published notice = %+v (%v), want UUID", notice, err)
	}
	get := func(token, query string, want int) []view {
		t.Helper()
		res := send(token, "GET", "/notices"+query, "", 200)
		var items []view
		if err := json.Unmarshal(res.Body.Bytes(), &items); err != nil || len(items) != want {
			t.Fatalf("notices %s = %+v (%v), want %d items", query, items, err, want)
		}
		return items
	}
	get(memberToken, "", 1)
	path := "/notices/" + notice.ID + "/receipt"
	send(memberToken, "PATCH", path, `{"state":"dismissed"}`, 409)
	send(memberToken, "PATCH", path, `{"state":"read"}`, 204)
	send(memberToken, "PATCH", path, `{"state":"dismissed"}`, 204)
	now = now.Add(time.Minute)
	send(memberToken, "PATCH", path, `{"state":"dismissed"}`, 204)
	get(memberToken, "", 0)
	history := get(memberToken, "?includeDismissed=true", 1)[0]
	if history.ReadAt == nil || history.DismissedAt == nil || !history.DismissedAt.Equal(now.Add(-time.Minute)) {
		t.Errorf("receipt = %+v, want recorded first read and first dismissal", history)
	}
	if got := get(adminToken, "", 1)[0]; got.ReadAt != nil || got.DismissedAt != nil {
		t.Errorf("other user inherited receipt: %+v", got)
	}
	start := now.Add(time.Minute)
	end := start.Add(time.Minute)
	timed, err := json.Marshal(map[string]any{
		"severity": "info", "title": "Scheduled", "body": "Upcoming work.", "effectiveAt": start, "expiresAt": end,
	})
	if err != nil {
		t.Fatal(err)
	}
	send(adminToken, "POST", "/admin/notices", string(timed), 201)
	get(adminToken, "", 1)
	now = start
	get(adminToken, "", 2)
	now = end
	get(adminToken, "", 1)
}
