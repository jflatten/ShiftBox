package contract_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestWorkspacePolicyUpdate verifies only a stopped, idle workspace can
// change network policy without rewriting its original launch evidence.
func TestWorkspacePolicyUpdate(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "workspace.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	now := time.Now().UTC()
	id, templateID, policyID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	token := strings.Repeat("A", 43)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
		generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		SELECT $1,id,'member',$2,1,'artifact','https://github.com/example/repo','.devcontainer/devcontainer.json',
		2,1024,40,'{"mode":"shared"}','stopped','running',1,'owned','fixture-vm',$3,$3,1 FROM installations`, id, templateID, now.Add(-2*time.Hour).Format(time.RFC3339Nano))
	exec(`INSERT INTO network_policies(id,name,mode,rules_json,required_exceptions_json,provider_capability_version)
		VALUES($1,'Outbound access','all','[]','[]','5.0.8')`, policyID)
	limit := int64(86400)
	verified, drift := 0, true
	handler := web.WorkspacePolicyHandler(db, origin, func() time.Time { return now }, web.WorkspaceLaunchPolicy{
		Capabilities: provider.Capabilities{Version: "5.0.8", NetworkModes: []string{"shared"}}, RetentionMaximum: &limit,
		VerifyNetwork: func(ctx context.Context, req provider.NetworkRequest) (provider.Inventory, error) {
			verified++
			if req.Ref.WorkspaceID != id || req.Ref.InstanceID != "fixture-vm" || req.Ref.Generation != 1 || req.Mode != "shared" {
				t.Errorf("network verification target = %+v, want exact stopped workspace with shared networking", req)
			}
			mode := req.Mode
			if drift {
				mode = "different"
			}
			return provider.Inventory{Ref: req.Ref, InstanceID: req.Ref.InstanceID, Power: provider.Stopped, NetworkMode: mode,
				ArtifactID: "artifact", Release: "release", ObservedAt: now}, ctx.Err()
		},
	}, nil)
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/workspaces/{workspaceId}", &handler)
	send := func(body, version string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/workspaces/"+id, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("If-Match", version)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("policy edit %s version %s = %d %s, want %d", body, version, w.Code, w.Body.String(), want)
		}
	}
	network := `{"networkPolicyId":"` + policyID + `"}`
	send(network, `"1"`, 409)
	exec("UPDATE workspaces SET observed_state='stopped' WHERE id=$1", id)
	exec(`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
		VALUES('policy-active-step',$1,'start',1,'member','policy-active-step','pending','now',1)`, id)
	send(network, `"1"`, 409)
	exec("UPDATE operations SET status='cancelled' WHERE id='policy-active-step'")
	send(network, `"2"`, 412)
	if verified != 0 {
		t.Fatal("stale or busy policy requests reached provider verification")
	}
	send(network, `"1"`, 409)
	var unchanged bool
	if err := db.QueryRowContext(ctx, `SELECT version=1 AND observed_state='stopped' AND network_policy_override_json IS NULL FROM workspaces WHERE id=$1`, id).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("failed verification left stopped workspace unchanged = %t (%v), want true", unchanged, err)
	}
	drift = false
	send(`{"retentionSeconds":86401}`, `"1"`, 409)
	send(`{"retentionSeconds":3600}`, `"1"`, 200)
	var initialDeadline string
	if err := db.QueryRowContext(ctx, "SELECT retention_deadline FROM workspaces WHERE id=$1", id).Scan(&initialDeadline); err != nil ||
		initialDeadline != now.Add(-time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("deadline without activity = %q (%v), want one hour after launch", initialDeadline, err)
	}
	exec("UPDATE workspaces SET last_activity_at=$1 WHERE id=$2", now.Add(-10*time.Minute).Format(time.RFC3339Nano), id)
	send(`{"retentionSeconds":3600,"networkPolicyId":"`+policyID+`"}`, `"2"`, 200)
	if verified != 2 {
		t.Errorf("network verification ran %d times, want two", verified)
	}
	var launch, current string
	var deadline *string
	var duration int64
	if err := db.QueryRowContext(ctx, `SELECT network_policy_snapshot_json,network_policy_override_json,retention_seconds,retention_deadline
		FROM workspaces WHERE id=$1`, id).Scan(&launch, &current, &duration, &deadline); err != nil ||
		launch != `{"mode":"shared"}` || !strings.Contains(current, policyID) || duration != 3600 ||
		deadline == nil || *deadline != now.Add(50*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("stored policy = %s, %s, %d, deadline %v (%v), want unchanged launch, selected policy and one hour from last activity", launch, current, duration, deadline, err)
	}
	send(`{"retentionSeconds":null}`, `"3"`, 200)
	// Preview configuration accepts only distinct TCP ports and is versioned
	// alongside the other workspace settings.
	send(`{"previewPorts":[8080,8080]}`, `"4"`, 400)
	send(`{"previewPorts":[0,65536]}`, `"4"`, 400)
	send(`{"previewPorts":[8080]}`, `"3"`, 412)
	send(`{"previewPorts":[9000,8080]}`, `"4"`, 200)
	var ports string
	if err := db.QueryRowContext(ctx, "SELECT preview_ports_json FROM workspaces WHERE id=$1", id).Scan(&ports); err != nil || ports != "[8080,9000]" {
		t.Fatalf("configured preview ports = %q (%v), want [8080,9000]", ports, err)
	}
	var cleared bool
	if err := db.QueryRowContext(ctx, "SELECT retention_seconds IS NULL AND retention_deadline IS NULL FROM workspaces WHERE id=$1", id).Scan(&cleared); err != nil || !cleared {
		t.Fatalf("retention cleared = %t (%v), want true", cleared, err)
	}
}
