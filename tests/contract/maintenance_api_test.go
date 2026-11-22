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
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestMaintenanceAdmission verifies admin authority, deadline validation,
// durable listing, and idempotency after the original deadline passes.
func TestMaintenanceAdmission(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	memberToken, adminToken := strings.Repeat("A", 43), strings.Repeat("B", 42)+"A"
	seedWorkspaceLaunch(t, db, uuid.NewString(), memberToken, now)
	const workspaceID = "00000000-0000-4000-8000-000000000089"
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
		platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version)
		SELECT $1,i.id,'member',t.id,1,'artifact','https://github.com/example/repo','devcontainer.json',2,1024,40,'{}',
		       'stopped','stopped',3,'fixture','now','now',1 FROM installations i CROSS JOIN project_templates t`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE sessions SET user_id='owner',token_digest=$1,csrf_digest=$2", auth.TokenDigest(adminToken), auth.TokenDigest(auth.CSRFToken(adminToken))); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	clock := func() time.Time { return now }
	list, create := web.MaintenanceHandlers(db, origin, clock, operation.NewJournal(db, clock, uuid.NewString))
	request := func(method, body, key, csrf string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, origin+"/api/v1/admin/maintenance", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: adminToken})
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		if method == "GET" {
			list.ServeHTTP(w, r)
		} else {
			create.ServeHTTP(w, r)
		}
		if w.Code != want {
			t.Fatalf("%s maintenance = %d %s; want %d", method, w.Code, w.Body, want)
		}
		return w.Body.String()
	}
	csrf := auth.CSRFToken(adminToken)
	const key = "maintenance-campaign-1"
	const campaign = `{"releaseId":"release","deadline":"2026-11-14T08:15:00Z"}`
	request("POST", campaign, key, "invalid", 403)
	if _, err := db.ExecContext(ctx, "UPDATE sessions SET user_id='member'"); err != nil {
		t.Fatal(err)
	}
	request("GET", "", "", "", 403)
	request("POST", campaign, key, csrf, 403)
	if _, err := db.ExecContext(ctx, "UPDATE sessions SET user_id='owner'"); err != nil {
		t.Fatal(err)
	}
	request("POST", `{"releaseId":"release","deadline":"2026-11-14T08:00:00Z"}`, key, csrf, 400)
	request("POST", `{"releaseId":"unknown","deadline":"2026-11-14T08:15:00Z"}`, key, csrf, 409)
	first := request("POST", campaign, key, csrf, 202)
	var accepted operation.Operation
	if err := json.Unmarshal([]byte(first), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("accepted = %s, %v; want a durable operation", first, err)
	}
	var generation int
	var desired, observed, state string
	if err := db.QueryRowContext(ctx, `SELECT generation,prior_desired_state,prior_observed_state,state
		FROM maintenance_targets WHERE campaign_id=$1 AND workspace_id=$2`, accepted.ID, workspaceID).
		Scan(&generation, &desired, &observed, &state); err != nil || generation != 3 || desired != "stopped" || observed != "stopped" || state != "pending" {
		t.Fatalf("maintenance target = %d %s/%s %s, %v; want pending with prior stopped state", generation, desired, observed, state, err)
	}
	now = now.Add(20 * time.Minute)
	if replay := request("POST", campaign, key, csrf, 202); replay != first {
		t.Fatalf("replayed operation = %s, want %s", replay, first)
	}
	request("POST", `{"releaseId":"release","deadline":"2026-11-14T08:45:00Z"}`, key, csrf, 409)
	items := request("GET", "", "", "", 200)
	if !strings.Contains(items, `"state":"scheduled"`) || !strings.Contains(items, `"operationId":"`+accepted.ID+`"`) {
		t.Errorf("campaign listing = %s; want the scheduled campaign and operation ID", items)
	}

	// A workspace owner sees their target warning without access to the
	// administrator's campaign list or ability to mutate the campaign.
	if _, err := db.ExecContext(ctx, "UPDATE sessions SET user_id='member'"); err != nil {
		t.Fatal(err)
	}
	_, detail := web.WorkspaceReadHandlers(db, origin, clock, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/workspaces/{workspaceId}", &detail)
	readWorkspace := func() *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", origin+"/api/v1/workspaces/"+workspaceID, nil)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: adminToken})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	response := readWorkspace()
	var view struct {
		Maintenance *struct {
			ReleaseID   string    `json:"releaseId"`
			Deadline    time.Time `json:"deadline"`
			State       string    `json:"state"`
			OperationID *string   `json:"operationId"`
		} `json:"maintenance"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); response.Code != 200 || err != nil ||
		view.Maintenance == nil || view.Maintenance.ReleaseID != "release" || view.Maintenance.State != "pending" ||
		!view.Maintenance.Deadline.Equal(time.Date(2026, 11, 14, 8, 15, 0, 0, time.UTC)) || view.Maintenance.OperationID != nil {
		t.Fatalf("owner workspace response = %d %s; want the pending maintenance warning with its scheduled release and deadline", response.Code, response.Body)
	}
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET generation=generation+1 WHERE id=$1", workspaceID); err != nil {
		t.Fatal(err)
	}
	response = readWorkspace()
	if response.Code != 200 || strings.Contains(response.Body.String(), `"maintenance":`) {
		t.Fatalf("workspace after generation change = %d %s; want no stale maintenance warning", response.Code, response.Body)
	}
}
