package web_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestKeepAliveAdministratorPolicy protects the organization settings from
// members, invalid duration lists, and stale administrator writes.
func TestKeepAliveAdministratorPolicy(t *testing.T) {
	db := keepAliveDatabase(t)
	session, principal, token := sessionHandlerFixture(t)
	handler := web.KeepAlivePolicyHandler(db, session.Origin, session.Now)
	handler.Lookup = func(_ context.Context, _ *sql.Tx, _ string) (auth.Principal, error) { return *principal, nil }
	request := func(body string, version int64) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, session.Origin+"/api/v1/admin/keep-alive", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", session.Origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("If-Match", strconv.Quote(strconv.FormatInt(version, 10)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	valid := `{"maximumMinutes":120,"allowedMinutes":[30,60,120],"defaultMinutes":60}`
	if got := request(valid, 1); got.Code != 403 {
		t.Fatalf("member update = %d %s, want403", got.Code, got.Body.String())
	}
	principal.User.Role = "administrator"
	if got := request(valid, 1); got.Code != 204 {
		t.Fatalf("administrator update = %d %s, want204", got.Code, got.Body.String())
	}
	if got := request(valid, 1); got.Code != 412 {
		t.Errorf("stale administrator update = %d %s, want412", got.Code, got.Body.String())
	}
	for _, body := range []string{
		`{"allowedMinutes":[0]}`,
		`{"maximumMinutes":60,"allowedMinutes":[30,120],"defaultMinutes":30}`,
		`{"maximumMinutes":60,"allowedMinutes":[30,0],"defaultMinutes":30}`,
		`{"maximumMinutes":120,"allowedMinutes":[30,30],"defaultMinutes":30}`,
		`{"maximumMinutes":120,"allowedMinutes":[30],"defaultMinutes":60}`,
	} {
		if got := request(body, 2); got.Code != 400 {
			t.Errorf("invalid policy %s = %d %s, want400", body, got.Code, got.Body.String())
		}
	}
	policy, err := workspace.ReadKeepAlivePolicy(t.Context(), db)
	if err != nil || policy.DefaultMinutes != 60 || policy.Version != 2 {
		t.Errorf("saved policy=%+v,%v,want default60 version2", policy, err)
	}
}

// TestWorkspaceKeepAliveDetail shows the effective current default after an
// administrator removes an owner's choice, and hides deadlines for Unlimited.
func TestWorkspaceKeepAliveDetail(t *testing.T) {
	db := keepAliveDatabase(t)
	session, principal, token := sessionHandlerFixture(t)
	_, handler := web.WorkspaceReadHandlers(db, session.Origin, session.Now, nil)
	handler.Lookup = func(_ context.Context, _ *sql.Tx, _ string) (auth.Principal, error) { return *principal, nil }
	if _, err := db.Exec(`UPDATE workspaces SET desired_state='running',observed_state='running',keep_alive_minutes=120;
	UPDATE installations SET keep_alive_allowed_minutes_json='[30,60,0]',keep_alive_default_minutes=60`); err != nil {
		t.Fatal(err)
	}
	read := func() struct {
		KeepAliveMinutes int64   `json:"keepAliveMinutes"`
		Override         *int64  `json:"keepAliveOverrideMinutes"`
		ShutdownAt       *string `json:"inactivityShutdownAt"`
		Idle             int64   `json:"inactivityMinutes"`
	} {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, session.Origin+"/api/v1/workspaces/00000000-0000-4000-8000-000000000035", nil)
		r.SetPathValue("workspaceId", "00000000-0000-4000-8000-000000000035")
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != 200 {
			t.Fatalf("detail=%d %s,want200", response.Code, response.Body.String())
		}
		var view struct {
			KeepAliveMinutes int64   `json:"keepAliveMinutes"`
			Override         *int64  `json:"keepAliveOverrideMinutes"`
			ShutdownAt       *string `json:"inactivityShutdownAt"`
			Idle             int64   `json:"inactivityMinutes"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	got := read()
	if got.KeepAliveMinutes != 60 || got.Override == nil || *got.Override != 120 || got.Idle != 60 || got.ShutdownAt == nil || *got.ShutdownAt != "2026-11-14T00:00:00Z" {
		t.Errorf("detail removed override=%+v,want default60,override120,idle60,deadline midnight", got)
	}
	if _, err := db.Exec("UPDATE workspaces SET keep_alive_minutes=0"); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.KeepAliveMinutes != 0 || got.ShutdownAt != nil {
		t.Errorf("unlimited detail=%+v,want0/no deadline", got)
	}
}

// TestWorkspaceKeepAliveSelection denies arbitrary values and preserves the
// inactivity clock when an owner changes or clears a workspace override.
func TestWorkspaceKeepAliveSelection(t *testing.T) {
	db := keepAliveDatabase(t)
	session, principal, token := sessionHandlerFixture(t)
	handler := web.WorkspacePolicyHandler(db, session.Origin, session.Now, web.WorkspaceLaunchPolicy{}, &proxy.Registry{})
	handler.Lookup = func(_ context.Context, _ *sql.Tx, _ string) (auth.Principal, error) { return *principal, nil }
	version := int64(1)
	request := func(value string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, session.Origin+"/api/v1/workspaces/00000000-0000-4000-8000-000000000035", strings.NewReader(`{"keepAliveMinutes":`+value+`}`))
		r.SetPathValue("workspaceId", "00000000-0000-4000-8000-000000000035")
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", session.Origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("If-Match", strconv.Quote(strconv.FormatInt(version, 10)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	for _, value := range []string{"17", "-1", "1.5", `"1h"`} {
		if got := request(value); got.Code != 400 && got.Code != 409 {
			t.Errorf("invalid selection %s=%d %s,want400/409", value, got.Code, got.Body.String())
		}
	}
	for _, value := range []string{"120", "0", "null"} {
		got := request(value)
		if got.Code != 200 {
			t.Fatalf("selection %s=%d %s,want200", value, got.Code, got.Body.String())
		}
		var view struct {
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		version = view.Version
	}
	var override sql.NullInt64
	var activity string
	if err := db.QueryRow("SELECT keep_alive_minutes,last_activity_at FROM workspaces").Scan(&override, &activity); err != nil {
		t.Fatal(err)
	}
	if override.Valid || activity != "2026-11-13T23:00:00Z" {
		t.Errorf("cleared override/activity=%+v/%s,want null/original activity", override, activity)
	}
}

func keepAliveDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, _, err := database.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "keep-alive.db"), database.BootstrapSeed{
		ID: "00000000-0000-4000-8000-000000000001", Organization: "ShiftBox", Origin: "https://management.test", IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`UPDATE installations SET state='active';
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	 VALUES('user','owner@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
	 owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
	 VALUES('artifact','test','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture','user','now','available');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
	 cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version,last_activity_at)
	 VALUES('00000000-0000-4000-8000-000000000035','00000000-0000-4000-8000-000000000001','user','template',1,'artifact','https://github.com/example/project','devcontainer.json',
	 2,1024,40,'{"mode":"shared"}','stopped','stopped',1,'owned','2026-11-13T00:00:00Z','2026-11-13T00:00:00Z',1,'2026-11-13T23:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
