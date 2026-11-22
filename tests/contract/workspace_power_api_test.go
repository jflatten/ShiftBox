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
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestWorkspacePowerAPI exercises version checks, replay, and transactional
// capacity accounting for a retained VM through the management endpoint,
// including rollback and replay when shutdown supersedes an active start.
func TestWorkspacePowerAPI(t *testing.T) {
	const origin = "https://management.test"
	const templateID = "00000000-0000-4000-8000-000000000003"
	const workspaceID = "00000000-0000-4000-8000-000000000004"
	const startKey = "workspace-start-key-one"
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "power.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,secret_names_json,
		desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		SELECT $1,id,'member',$2,1,'artifact','https://github.com/example/repo','.devcontainer/devcontainer.json',
			2,1024,40,'{"mode":"shared"}','[]','stopped','stopped',3,'owned','vm','now','now',5 FROM installations`, workspaceID, templateID)
	exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		VALUES($1,2,1024,40,'released','reserved','now','now')`, workspaceID)

	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	power := web.WorkspacePowerHandler(db, origin, clock, journal, &proxy.Registry{})
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/workspaces/{workspaceId}/actions/{action}", &power)
	request := func(action, version, key, body string, status int, code string) operation.Operation {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces/"+workspaceID+"/actions/"+action, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("If-Match", version)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var op operation.Operation
		var problem web.Problem
		if w.Code != status || (code != "" && (json.Unmarshal(w.Body.Bytes(), &problem) != nil || problem.Code != code)) ||
			(status == 202 && (json.Unmarshal(w.Body.Bytes(), &op) != nil || op.ID == "")) {
			t.Fatalf("%s returned %d %s, want %d %s", action, w.Code, w.Body.String(), status, code)
		}
		return op
	}
	checkState := func(desired, compute string, version int64, operations int) {
		t.Helper()
		var gotDesired, observed, gotCompute, disk string
		var generation, gotVersion int64
		var count int
		err := db.QueryRowContext(ctx, `SELECT w.desired_state,w.observed_state,w.generation,w.version,
			r.cpu_memory_state,r.disk_state,(SELECT COUNT(*) FROM operations) FROM workspaces w
			JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, workspaceID).
			Scan(&gotDesired, &observed, &generation, &gotVersion, &gotCompute, &disk, &count)
		if err != nil || gotDesired != desired || observed != "stopped" || generation != 3 ||
			gotVersion != version || gotCompute != compute || disk != "reserved" || count != operations {
			t.Fatalf("state = %s/%s generation %d version %d capacity %s/%s operations %d, err %v",
				gotDesired, observed, generation, gotVersion, gotCompute, disk, count, err)
		}
	}

	request("start", "", startKey, "", 428, "request.precondition_required")
	// Rebuilding a stopped workspace must not reserve capacity or start its VM.
	request("rebuild", `"5"`, "workspace-rebuild-one", "", 409, "workspace.action_unavailable")
	request("start", `"4"`, startKey, "", 412, "request.version_mismatch")
	request("start", `"5"`, startKey, `{"ownerId":"someone"}`, 400, "request.invalid_body")
	exec("UPDATE capacity_budgets SET cpu_limit=1")
	request("start", `"5"`, startKey, "", 409, "workspace.capacity_exceeded")
	checkState("stopped", "released", 5, 0)
	exec("UPDATE capacity_budgets SET cpu_limit=2")
	exec(`CREATE TRIGGER fail_power_journal BEFORE INSERT ON operation_events
		BEGIN SELECT RAISE(ABORT, 'journal unavailable'); END`)
	request("start", `"5"`, startKey, "", 500, "")
	checkState("stopped", "released", 5, 0)
	exec("DROP TRIGGER fail_power_journal")
	start := request("start", `"5"`, startKey, "", 202, "")
	if start.Type != "start" || start.Status != "pending" {
		t.Fatalf("unexpected accepted start: %+v", start)
	}
	if stored, err := journal.Get(ctx, start.ID); err != nil || stored.Generation != 3 ||
		stored.OriginatingAction != "workspace.start" {
		t.Fatalf("stored start = %+v, %v, want current provider generation and start authority", stored, err)
	}
	checkState("running", "reserved", 6, 1)
	if replay := request("start", `"5"`, startKey, "{}", 202, ""); replay.ID != start.ID {
		t.Fatalf("replay returned %s, want %s", replay.ID, start.ID)
	}
	request("stop", `"6"`, startKey, "", 409, "workspace.action_conflict")
	exec(`CREATE TRIGGER fail_stop_journal BEFORE INSERT ON operation_events
		WHEN NEW.operation_id<>'` + start.ID + `' BEGIN SELECT RAISE(ABORT, 'journal unavailable'); END`)
	request("stop", `"6"`, "workspace-stop-key-one", "", 500, "")
	checkState("running", "reserved", 6, 1)
	if previous, err := journal.Get(ctx, start.ID); err != nil || previous.Status != "pending" {
		t.Fatalf("start after rejected shutdown = %s, %v, want pending", previous.Status, err)
	}
	exec("DROP TRIGGER fail_stop_journal")
	stop := request("stop", `"6"`, "workspace-stop-key-one", "", 202, "")
	if stop.Type != "stop" {
		t.Fatalf("unexpected accepted stop: %+v", stop)
	}
	if previous, err := journal.Get(ctx, start.ID); err != nil || previous.Status != "cancelled" {
		t.Fatalf("superseded start = %s, %v, want cancelled", previous.Status, err)
	}
	if replay := request("stop", `"6"`, "workspace-stop-key-one", "", 202, ""); replay.ID != stop.ID {
		t.Fatalf("shutdown replay returned %s, want %s", replay.ID, stop.ID)
	}
	checkState("stopped", "reserved", 7, 2)
	// Rebuild keeps the same running VM and returns the original operation on replay.
	exec("UPDATE operations SET status='succeeded' WHERE id=$1", stop.ID)
	exec("UPDATE workspaces SET desired_state='running',observed_state='running' WHERE id=$1", workspaceID)
	rebuild := request("rebuild", `"7"`, "workspace-rebuild-one", "", 202, "")
	if rebuild.Type != "rebuild" || len(rebuild.Steps) != 3 || rebuild.Steps[0].Key != "configure" {
		t.Fatalf("rebuild admission = %+v, want configure, verify IDE, and finish setup", rebuild)
	}
	if replay := request("rebuild", `"7"`, "workspace-rebuild-one", "", 202, ""); replay.ID != rebuild.ID {
		t.Fatalf("rebuild replay returned %s, want %s", replay.ID, rebuild.ID)
	}
	request("delete", `"8"`, "workspace-delete-one", `{"confirmDataLoss":true}`, 409, "workspace.operation_in_progress")
	request("stop", `"8"`, "workspace-stop-key-two", "", 202, "")
	exec("UPDATE users SET membership_state='disabled' WHERE id='member'")
	request("start", `"5"`, startKey, "", 401, "auth.required")
}
