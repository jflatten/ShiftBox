package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestWorkspaceProbeResultsAPI verifies that workspace detail exposes only
// fresh observations from a running workspace's current registration.
func TestWorkspaceProbeResultsAPI(t *testing.T) {
	const origin = "https://management.test"
	const templateID = "00000000-0000-4000-8000-000000000003"
	const workspaceID = "00000000-0000-4000-8000-000000000004"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "probes.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	seedStoppedAPIWorkspace(t, db, templateID, workspaceID)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	report := []workspace.ProbeResult{{ID: workspace.ProbeCodeServer, Status: workspace.ProbeError}}
	exec(`INSERT INTO guest_registrations (workspace_id,generation,state,probe_reported_at,probe_results_json)
		VALUES ($1,1,'active',$2,'[{"id":"code-server","status":"error"}]')`, workspaceID, now.Format("2006-01-02T15:04:05.000000000Z"))
	_, detail := web.WorkspaceReadHandlers(db, origin, func() time.Time { return now }, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/workspaces/{workspaceId}", &detail)
	check := func(want []workspace.ProbeResult) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, origin+"/api/v1/workspaces/"+workspaceID, nil)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		var data struct {
			Probes []workspace.ProbeResult `json:"probes"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &data); response.Code != http.StatusOK ||
			err != nil || data.Probes == nil || !slices.Equal(data.Probes, want) {
			t.Fatalf("workspace detail = %d %s (%v), want probes %+v", response.Code, response.Body, err, want)
		}
	}
	check(nil)
	exec("UPDATE workspaces SET desired_state='running',observed_state='running' WHERE id=$1", workspaceID)
	check(report)
	exec("UPDATE workspaces SET generation=2 WHERE id=$1", workspaceID)
	check(nil)
	exec("UPDATE workspaces SET generation=1 WHERE id=$1", workspaceID)
	now = now.Add(91 * time.Second)
	check(nil)
}
