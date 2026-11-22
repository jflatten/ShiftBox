package contract_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestIDEHumanActivity requires current IDE authority and a same-origin input
// notification. Script loads, idle connections, previews and revoked cookies cannot renew inactivity.
func TestIDEHumanActivity(t *testing.T) {
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "input.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(t.Context(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	const id = "00000000-0000-4000-8000-000000000004"
	const templateID = "00000000-0000-4000-8000-000000000003"
	seedWorkspaceLaunch(t, db, templateID, strings.Repeat("A", 43), now)
	seedStoppedAPIWorkspace(t, db, templateID, id)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE workspaces SET desired_state='running',observed_state='running',preview_ports_json='[8080]' WHERE id=$1", id)
	exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
		VALUES('boot',$1,'start',1,'member','human-activity-boot','succeeded',$2,1,'workspace.start')`, id, now.Format(time.RFC3339Nano))
	exec(`INSERT INTO operation_steps(operation_id,step_index,step_key,label,status) VALUES('boot',0,'verify_ide','Verify IDE','succeeded')`)
	hosts, err := config.NewServiceHosts("https://management.test", "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	forwarded := 0
	service := web.ServiceProxyHandler(db, hosts, &proxy.Registry{}, func() time.Time { return now }, func(w http.ResponseWriter, _ *http.Request, _ string, _ int64, _, _ string) {
		forwarded++
		w.WriteHeader(204)
	})
	issue := func(name string) (string, *http.Cookie) {
		t.Helper()
		origin := "https://" + id + ".ide.test"
		if name != "ide" {
			origin = "https://" + name + "--" + id + ".preview.test"
		}
		var token string
		err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
			var err error
			token, _, err = proxy.IssueOriginGrant(t.Context(), tx, proxy.OriginAuthority{Scope: proxy.OriginScope{WorkspaceID: id, Generation: 1, Service: name, Origin: origin}, UserID: "member", SessionID: "launch-session", Permission: "workspace.connect_owner"}, now)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		service.ServeHTTP(response, httptest.NewRequest("GET", origin+web.ServiceEntryPath+"?grant="+token, nil))
		cookies := response.Result().Cookies()
		if response.Code != 200 || len(cookies) != 1 {
			t.Fatalf("entry=%d, cookies=%d, want 200 and one cookie", response.Code, len(cookies))
		}
		return origin, cookies[0]
	}
	origin, cookie := issue("ide")
	request := func(method, path, from, header string, cookie *http.Cookie, want int) {
		t.Helper()
		req := httptest.NewRequest(method, origin+path, strings.NewReader("{}"))
		if from != "" {
			req.Header.Set("Origin", from)
		}
		if header != "" {
			req.Header.Set("X-ShiftBox-Activity", header)
		}
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		service.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s =%d %s, want %d", method, path, response.Code, response.Body, want)
		}
	}
	request("GET", proxy.HumanActivityScriptPath, "", "", cookie, 200)
	request("POST", proxy.HumanActivityPath, origin, "", cookie, 403)
	request("POST", proxy.HumanActivityPath, "https://other.test", "input", cookie, 403)
	request("POST", proxy.HumanActivityPath, origin, "input", nil, 403)
	var activity sql.NullString
	if err := db.QueryRow("SELECT last_activity_at FROM workspaces WHERE id=$1", id).Scan(&activity); err != nil || activity.Valid {
		t.Fatalf("noninput activity=%v (%v), want unset", activity, err)
	}
	request("POST", proxy.HumanActivityPath, origin, "input", cookie, 204)
	if err := db.QueryRow("SELECT last_activity_at FROM workspaces WHERE id=$1", id).Scan(&activity); err != nil || activity.String != now.Format(time.RFC3339Nano) {
		t.Fatalf("human activity=%v (%v), want controller receipt", activity, err)
	}
	if forwarded != 0 {
		t.Errorf("activity forwarded=%d, want zero", forwarded)
	}
	previewOrigin, previewCookie := issue("preview-8080")
	savedOrigin := origin
	origin = previewOrigin
	request("POST", proxy.HumanActivityPath, origin, "input", previewCookie, 404)
	origin = savedOrigin
	exec("UPDATE sessions SET revoked_at=$1 WHERE id='launch-session'", now.Format(time.RFC3339Nano))
	request("POST", proxy.HumanActivityPath, origin, "input", cookie, 403)
}
