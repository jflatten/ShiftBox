package contract_test

import (
	"bufio"
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
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestWorkspaceLaunchAPI exercises session and template authorization,
// committed launch and replay, and an over-capacity request through HTTP.
func TestWorkspaceLaunchAPI(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "launch.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const templateID = "00000000-0000-4000-8000-000000000003"
	const body = `{"templateId":"` + templateID + `","templateRevision":1,"name":"MVP development"}`
	token := strings.Repeat("A", 43)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	policyID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO network_policies(id,name,mode,rules_json,required_exceptions_json,provider_capability_version)
		VALUES($1,'Outbound access','all','[]','[]','5.0.8')`, policyID); err != nil {
		t.Fatal(err)
	}
	// Keep the first revision on AMD64 and bind the ARM64 image to a new revision.
	if _, err := db.Exec(`INSERT INTO platform_artifacts
		(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,
		provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		SELECT 'arm-artifact','release-arm64','arm64',platform_version,guest_release,protocol_version,owned_resources_json,digests_json,
		provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state
		FROM platform_artifacts WHERE id='artifact'`); err != nil {
		t.Fatal(err)
	}
	// Template revisions are immutable, including their policy reference.
	if _, err := db.Exec(`INSERT INTO template_revisions(template_id,revision,repository_url,devcontainer_path,resource_profile_id,platform_artifact_id,
		network_policy_id,secret_names_json,created_by,created_at)
		SELECT template_id,2,repository_url,devcontainer_path,resource_profile_id,'arm-artifact',$1,secret_names_json,created_by,created_at
		FROM template_revisions WHERE template_id=$2 AND revision=1`, policyID, templateID); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	available := uint64(1 << 40)
	handler := web.WorkspaceLaunchHandler(db, origin, clock, journal, web.WorkspaceLaunchPolicy{
		Capabilities: provider.Capabilities{Create: true, Start: true, Architecture: "amd64", Architectures: []string{"amd64", "arm64"}, Version: "5.0.8", NetworkModes: []string{"shared"},
			Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 1 << 20},
			Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 << 20, DiskBytes: 100 << 20}},
		Protocols: []string{"1"}, AvailableDisk: func() (uint64, error) { return available, nil },
	})
	request := func(payload, key, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces", strings.NewReader(payload))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	csrf := auth.CSRFToken(token)
	key := "workspace-launch-key-one"
	check := func(response *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var problem web.Problem
		if response.Code != status || (code != "" && (json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.Code != code)) {
			t.Fatalf("launch returned %d %s, want %d %s", response.Code, response.Body.String(), status, code)
		}
	}
	check(request(body, key, "wrong"), 403, "auth.invalid_origin_or_csrf")
	check(request(body, key, csrf), 403, "auth.forbidden")
	check(request(strings.TrimSuffix(body, "}")+`,"ownerId":"owner"}`, key, csrf), 400, "request.invalid_body")
	if _, err := db.Exec(`INSERT INTO template_grants (template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
		VALUES($1,'member',1,'now','owner','now')`, templateID); err != nil {
		t.Fatal(err)
	}
	withRetention := strings.TrimSuffix(body, "}") + `,"retentionSeconds":3600}`
	check(request(withRetention, key, csrf), 409, "workspace.retention_unavailable")
	selectedBody := strings.Replace(body, `"templateRevision":1`, `"templateRevision":2`, 1)
	selectedRetention := strings.Replace(withRetention, `"templateRevision":1`, `"templateRevision":2`, 1)
	// The template's old image still speaks protocol 1, but its agent is
	// outside the deployed release window and cannot enroll on a new VM.
	if _, err := db.Exec("UPDATE installations SET active_release='current',min_guest_release='older'"); err != nil {
		t.Fatal(err)
	}
	check(request(selectedBody, key, csrf), 409, "workspace.platform_incompatible")
	if _, err := db.Exec("UPDATE installations SET min_guest_release='release'"); err != nil {
		t.Fatal(err)
	}
	available = 1
	check(request(selectedBody, key, csrf), 409, "workspace.capacity_exceeded")
	available = 1 << 40
	accepted := request(selectedBody, key, csrf)
	check(accepted, 202, "")
	var op operation.Operation
	if err := json.Unmarshal(accepted.Body.Bytes(), &op); err != nil || op.WorkspaceID == nil || op.ID == "" {
		t.Fatalf("launch response = %+v, %v, want durable operation", op, err)
	}
	// Disk pressure blocks new allocations, but it cannot hide a committed launch.
	available = 1
	replayed := request(selectedBody, key, csrf)
	available = 1 << 40
	check(replayed, 202, "")
	check(request(strings.Replace(selectedBody, "MVP development", "Other workspace", 1), key, csrf), 409, "workspace.launch_conflict")
	var savedName, artifact string
	if err := db.QueryRowContext(ctx, "SELECT name,platform_artifact_id FROM workspaces WHERE id=$1", *op.WorkspaceID).
		Scan(&savedName, &artifact); err != nil || savedName != "MVP development" || artifact != "arm-artifact" {
		t.Fatalf("stored workspace name and platform = %q/%q (%v), want MVP development and arm-artifact", savedName, artifact, err)
	}
	if replayed.Body.String() != accepted.Body.String() {
		t.Error("retry returned a different operation")
	}
	check(request(selectedRetention, key, csrf), 409, "workspace.launch_conflict")
	capacity := request(body, "workspace-launch-key-two", csrf)
	check(capacity, http.StatusConflict, "workspace.capacity_exceeded")
	var capacityProblem web.Problem
	if err := json.Unmarshal(capacity.Body.Bytes(), &capacityProblem); err != nil || capacityProblem.Fields["resource"] != "cpu" {
		t.Errorf("over-capacity launch resource = %q (%v), want cpu", capacityProblem.Fields["resource"], err)
	}
	if _, err := db.Exec("UPDATE resource_profiles SET enabled=0 WHERE id='profile'"); err != nil {
		t.Fatal(err)
	}
	check(request(selectedBody, key, csrf), 202, "")
	check(request(body, "workspace-launch-key-two", csrf), 409, "workspace.template_unavailable")
	if _, err := db.Exec("UPDATE template_grants SET revoked_at='now' WHERE template_id=$1", templateID); err != nil {
		t.Fatal(err)
	}
	check(request(body, "workspace-launch-key-tri", csrf), 403, "auth.forbidden")
	check(request(body, key, csrf), 403, "auth.forbidden")
	var owner, network string
	var reservations, operations int
	if err := db.QueryRow(`SELECT owner_user_id,network_policy_snapshot_json,(SELECT COUNT(*) FROM reservations),(SELECT COUNT(*) FROM operations)
		FROM workspaces WHERE id=$1`, *op.WorkspaceID).Scan(&owner, &network, &reservations, &operations); err != nil ||
		owner != "member" || !strings.Contains(network, policyID) || !strings.Contains(network, `"mode":"shared"`) || reservations != 1 || operations != 1 {
		t.Fatalf("committed launch = %s, network %s, %d reservations, %d operations, %v; want member, selected shared policy, one reservation and one operation", owner, network, reservations, operations, err)
	}
	detail, stream := web.OperationHandlers(db, origin, clock, journal, &proxy.Registry{})
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/operations/{operationId}", &detail)
	mux.Handle("GET /api/v1/operations/{operationId}/events", &stream)
	graceUntil := now.Add(10 * time.Minute)
	listWorkspace, getWorkspace := web.WorkspaceReadHandlers(db, origin, clock, func(time.Time) web.WorkspaceRetentionStatus {
		return web.WorkspaceRetentionStatus{State: "grace", GraceUntil: &graceUntil}
	})
	mux.Handle("GET /api/v1/workspaces", &listWorkspace)
	mux.Handle("GET /api/v1/workspaces/{workspaceId}", &getWorkspace)
	path := "/api/v1/operations/" + op.ID
	lookup := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, origin+path, nil)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	response := lookup(path)
	check(response, 200, "")
	// Launch timing state is visible to the owner alongside the same durable workflow.
	var timingDetail struct {
		operation.Operation
		LaunchPerformance *struct {
			Steps []struct{ Key string }
		} `json:"launchPerformance"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &timingDetail); err != nil ||
		timingDetail.ID != op.ID || timingDetail.Version != op.Version || timingDetail.LaunchPerformance == nil ||
		len(timingDetail.LaunchPerformance.Steps) != len(op.Steps) {
		t.Fatalf("operation detail omitted launch timings: %+v, %v", timingDetail, err)
	}
	workspacePath := "/api/v1/workspaces/" + *op.WorkspaceID
	if _, err := db.Exec("UPDATE workspaces SET retention_deadline=$1 WHERE id=$2", now.Format(time.RFC3339Nano), *op.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	response = lookup(workspacePath)
	check(response, 200, "")
	var view struct {
		OwnerID            string                       `json:"ownerId"`
		PlatformRelease    string                       `json:"platformRelease"`
		TemplateRevision   int                          `json:"templateRevision"`
		DesiredState       string                       `json:"desiredState"`
		ObservedState      string                       `json:"observedState"`
		CurrentOperationID string                       `json:"currentOperationId"`
		LaunchOperationID  string                       `json:"launchOperationId"`
		Retention          web.WorkspaceRetentionStatus `json:"retention"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil ||
		view.OwnerID != "member" || view.DesiredState != "running" || view.ObservedState != "unknown" ||
		view.PlatformRelease != "release-arm64" || view.TemplateRevision != 2 ||
		view.CurrentOperationID != op.ID || view.LaunchOperationID != op.ID || response.Header().Get("ETag") != `"1"` ||
		view.Retention.State != "grace" || view.Retention.GraceUntil == nil || !view.Retention.GraceUntil.Equal(graceUntil) {
		t.Fatalf("workspace view = %+v, %v, want member-owned pending launch", view, err)
	}
	check(lookup("/api/v1/workspaces?state=running"), 200, "")
	if response := lookup("/api/v1/workspaces?state=running"); response.Body.String() != "{\"items\":[],\"nextCursor\":null}\n" {
		t.Errorf("observed running filter included an unprovisioned workspace: %s", response.Body)
	}
	if response := lookup("/api/v1/workspaces?state=unknown"); !strings.Contains(response.Body.String(), *op.WorkspaceID) {
		t.Errorf("owner list omitted workspace: %s", response.Body)
	}
	claim, err := journal.Claim(ctx, op.ID, "fixture-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 0); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		cursor string
		status int
		code   string
	}{
		{cursor: "invalid", status: http.StatusBadRequest, code: "request.invalid_event_cursor"},
		{cursor: "9999", status: http.StatusConflict, code: "operation.invalid_event_cursor"},
	} {
		t.Run("event cursor "+scenario.cursor, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, origin+path+"/events", nil)
			req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
			req.Header.Set("Last-Event-ID", scenario.cursor)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			var problem web.Problem
			err := json.Unmarshal(response.Body.Bytes(), &problem)
			if response.Code != scenario.status || err != nil || problem.Code != scenario.code {
				t.Errorf("operation event cursor %q returned %d %q (%v), want %d %q", scenario.cursor, response.Code, problem.Code, err, scenario.status, scenario.code)
			}
		})
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	streamRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	streamRequest.Header.Set("Last-Event-ID", "1")
	streamResponse, err := (&http.Client{Timeout: 3 * time.Second}).Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != 200 || streamResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("operation events = %d %s, want SSE stream", streamResponse.StatusCode, streamResponse.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(streamResponse.Body)
	var resumed bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			resumed = line == "id: 2"
			break
		}
	}
	if !resumed {
		t.Fatalf("stream did not resume at the first unseen event: %v", scanner.Err())
	}
	if err := streamResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	// After a terminal failure the workspace has no active operation, but
	// a fresh detail read must still identify the failed workflow.
	if _, err := db.Exec(`UPDATE operations SET status='failed' WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	checkOperationReferences := func(current, failed, launch string) {
		t.Helper()
		response := lookup(workspacePath)
		var refs struct {
			CurrentOperationID    string `json:"currentOperationId"`
			LastFailedOperationID string `json:"lastFailedOperationId"`
			LaunchOperationID     string `json:"launchOperationId"`
		}
		err := json.Unmarshal(response.Body.Bytes(), &refs)
		if response.Code != 200 || err != nil || refs.CurrentOperationID != current || refs.LastFailedOperationID != failed || refs.LaunchOperationID != launch {
			t.Errorf("workspace detail operation references = %d %+v (%v), want active %q, failed %q, latest launch or start %q",
				response.Code, refs, err, current, failed, launch)
		}
	}
	checkOperationReferences("", op.ID, op.ID)
	now = now.Add(time.Minute)
	var generation int64
	if err := db.QueryRow("SELECT generation FROM workspaces WHERE id=$1", *op.WorkspaceID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	later, err := journal.Create(ctx, operation.Request{Actor: "member", Key: "newer-attempt-operation", Type: "start",
		OriginatingAction: "workspace.start", WorkspaceID: op.WorkspaceID, Generation: generation,
		Steps: []operation.Step{{Key: "start", Label: "Start workspace"}}})
	if err != nil {
		t.Fatal(err)
	}
	// The pending start supersedes the original launch immediately, even before
	// the first timing arrives. The same operation remains selected after completion.
	checkOperationReferences(later.ID, "", later.ID)
	startDetail := lookup("/api/v1/operations/" + later.ID)
	check(startDetail, 200, "")
	timingDetail.LaunchPerformance = nil
	if err := json.Unmarshal(startDetail.Body.Bytes(), &timingDetail); err != nil ||
		timingDetail.ID != later.ID || timingDetail.LaunchPerformance == nil ||
		len(timingDetail.LaunchPerformance.Steps) != 1 || timingDetail.LaunchPerformance.Steps[0].Key != "start" {
		t.Errorf("operation detail for pending start = %+v, %v, want fresh start timing", timingDetail, err)
	}
	laterClaim, err := journal.Claim(ctx, later.ID, "fixture-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, laterClaim, 0); err != nil {
		t.Fatal(err)
	}
	if err := journal.CompleteStep(ctx, laterClaim, 0, "Verified"); err != nil {
		t.Fatal(err)
	}
	checkOperationReferences("", "", later.ID)
	// Another member with a valid session cannot use an operation ID to
	// discover a workspace or subscribe to its progress.
	if _, err := db.Exec("UPDATE users SET role='member' WHERE id='owner'"); err != nil {
		t.Fatal(err)
	}
	other, otherToken, err := auth.NewSession("other-session", "owner", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return auth.NewStore(db).SaveSession(ctx, tx, other) }); err != nil {
		t.Fatal(err)
	}
	token = otherToken
	check(lookup(path), 404, "operation.not_found")
	check(lookup(workspacePath), 404, "workspace.not_found")
	if response := lookup("/api/v1/workspaces"); response.Code != 200 || strings.Contains(response.Body.String(), *op.WorkspaceID) {
		t.Errorf("other member saw a foreign workspace: %d %s", response.Code, response.Body)
	}
	check(lookup("/api/v1/workspaces?cursor=invalid"), 400, "request.invalid_filter")
	if _, err := db.Exec("UPDATE users SET role='administrator' WHERE id='owner'"); err != nil {
		t.Fatal(err)
	}
	check(lookup(workspacePath), 200, "")
	if response := lookup(path); response.Code != 200 || !strings.Contains(response.Body.String(), `"launchPerformance"`) {
		t.Errorf("administrator launch timing = %d %s", response.Code, response.Body)
	}
	if response := lookup("/api/v1/workspaces"); response.Code != 200 || !strings.Contains(response.Body.String(), *op.WorkspaceID) {
		t.Errorf("administrator list omitted workspace: %d %s", response.Code, response.Body)
	}
	if response := lookup("/api/v1/workspaces?cursor=" + *op.WorkspaceID); response.Body.String() != "{\"items\":[],\"nextCursor\":null}\n" {
		t.Errorf("cursor replayed the previous item: %d %s", response.Code, response.Body)
	}
	// Hidden history must exclude only confirmed deletion. A pending deletion
	// remains visible so an operator can follow cleanup and uncertain outcomes.
	check(lookup("/api/v1/workspaces?excludeDeleted=invalid"), 400, "request.invalid_filter")
	for _, observed := range []string{"deleting", "deleted"} {
		if _, err := db.Exec("UPDATE workspaces SET desired_state='deleted',observed_state=$1 WHERE id=$2", observed, *op.WorkspaceID); err != nil {
			t.Fatal(err)
		}
		response := lookup("/api/v1/workspaces?excludeDeleted=true")
		present := strings.Contains(response.Body.String(), *op.WorkspaceID)
		if response.Code != 200 || present != (observed == "deleting") {
			t.Errorf("excludeDeleted for %s returned %d, workspace present=%t, want 200 and present=%t", observed, response.Code, present, observed == "deleting")
		}
	}
	if response := lookup("/api/v1/workspaces?excludeDeleted=false&state=deleted"); response.Code != 200 || !strings.Contains(response.Body.String(), *op.WorkspaceID) {
		t.Errorf("explicit deleted history returned %d %s, want deleted workspace", response.Code, response.Body)
	}
	if _, err := db.Exec("UPDATE users SET role='member' WHERE id='owner'"); err != nil {
		t.Fatal(err)
	}
	check(lookup(path), 404, "operation.not_found")
	check(lookup(path+"/events"), 404, "operation.not_found")
	check(lookup("/api/v1/operations/"+uuid.NewString()), 404, "operation.not_found")
}

// TestWorkspaceDeleteAndUpdateAPI covers the published lifecycle actions for
// an owned stopped workspace, including explicit deletion and unavailable maintenance.
func TestWorkspaceDeleteAndUpdateAPI(t *testing.T) {
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
	templateID, workspaceID := uuid.NewString(), uuid.NewString()
	token := strings.Repeat("A", 43)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	seedStoppedAPIWorkspace(t, db, templateID, workspaceID)
	clock := func() time.Time { return now }
	registry := &proxy.Registry{}
	closed := false
	pollClosed, streamClosed := false, false
	if _, err := registry.Register(proxy.Scope{WorkspaceID: workspaceID, Service: "ide"}, func() error { return nil }, func() error {
		closed = true
		var revoked bool
		if err := db.QueryRowContext(ctx, "SELECT revoked_at IS NOT NULL FROM origin_grants WHERE id='delete-fixture'").Scan(&revoked); err != nil || !revoked {
			t.Errorf("workspace closure saw origin revoked=%t (%v), want committed revocation", revoked, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		scope  proxy.Scope
		closed *bool
	}{
		{proxy.Scope{WorkspaceID: workspaceID, GuestPoll: true}, &pollClosed},
		{proxy.Scope{WorkspaceID: workspaceID, SessionID: "launch-session"}, &streamClosed},
	} {
		if _, err := registry.Register(entry.scope, func() error { return nil }, func() error {
			*entry.closed = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO origin_grants(id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,permission_basis,expires_at)
		VALUES('delete-fixture','fixture-digest','member','launch-session',$1,1,'ide','https://ide.test','workspace.connect_owner',$2)`, workspaceID, now.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, clock, uuid.NewString)
	handler := web.WorkspacePowerHandler(db, origin, clock, journal, registry)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/workspaces/{workspaceId}/actions/{action}", &handler)
	request := func(workspaceID, action, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces/"+workspaceID+"/actions/"+action, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("If-Match", `"1"`)
		r.Header.Set("Idempotency-Key", "workspace-action-"+action)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	// An unscheduled update cannot create a workspace operation.
	t.Run("update without scheduled maintenance", func(t *testing.T) {
		response := request(workspaceID, "update", "")
		if response.Code != http.StatusConflict {
			t.Errorf("workspace update without scheduled maintenance returned %d %s, want %d", response.Code, response.Body, http.StatusConflict)
		}
		var updates int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE workspace_id=$1", workspaceID).Scan(&updates); err != nil || updates != 0 {
			t.Fatalf("workspace update without scheduled maintenance recorded %d operations (%v), want 0", updates, err)
		}
	})
	t.Run("explicit deletion", func(t *testing.T) {
		withoutConfirmation := request(workspaceID, "delete", `{"confirmDataLoss":false}`)
		var operationCount int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM operations").Scan(&operationCount); err != nil {
			t.Fatal(err)
		}
		if withoutConfirmation.Code < 400 || withoutConfirmation.Code >= 500 || operationCount != 0 {
			t.Fatalf("unconfirmed deletion returned %d with %d operations, want client rejection without a recorded operation", withoutConfirmation.Code, operationCount)
		}
		accepted := request(workspaceID, "delete", `{"confirmDataLoss":true}`)
		if accepted.Code != http.StatusAccepted {
			t.Fatalf("confirmed deletion returned %d %s, want %d", accepted.Code, accepted.Body, http.StatusAccepted)
		}
		if !closed || pollClosed || streamClosed {
			t.Errorf("deletion closed IDE/poll/management %t/%t/%t, want true/false/false", closed, pollClosed, streamClosed)
		}
		var op operation.Operation
		if err := json.Unmarshal(accepted.Body.Bytes(), &op); err != nil || op.ID == "" || op.Type != "delete" {
			t.Fatalf("confirmed deletion returned %s (%v), want a durable delete operation", accepted.Body, err)
		}
		var desired, observed, disk string
		var committed bool
		if err := db.QueryRowContext(ctx, `SELECT w.desired_state,w.observed_state,r.disk_state,w.deletion_commit_at IS NOT NULL FROM workspaces w
			JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, workspaceID).Scan(&desired, &observed, &disk, &committed); err != nil {
			t.Fatal(err)
		}
		if desired != "deleted" || observed != "stopped" || disk != "reserved" || !committed {
			t.Errorf("deletion admission produced %s/%s with %s disk and commit %t, want deleted/stopped, reserved disk and durable commit", desired, observed, disk, committed)
		}
		var replayed operation.Operation
		replay := request(workspaceID, "delete", `{"confirmDataLoss":true}`)
		if err := json.Unmarshal(replay.Body.Bytes(), &replayed); replay.Code != http.StatusAccepted || err != nil || replayed.ID != op.ID {
			t.Errorf("deletion replay returned %d and operation %q (%v), want %d and %q", replay.Code, replayed.ID, err, http.StatusAccepted, op.ID)
		}
	})
	// An owner can request an early update for one stopped workspace and retry
	// the request without starting duplicate work or touching other targets.
	t.Run("update before the scheduled deadline", func(t *testing.T) {
		targetID, otherID := uuid.NewString(), uuid.NewString()
		seedStoppedAPIWorkspace(t, db, templateID, targetID)
		seedStoppedAPIWorkspace(t, db, templateID, otherID)
		var installation string
		var campaign operation.Operation
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, "SELECT id FROM installations").Scan(&installation); err != nil {
				return err
			}
			var err error
			campaign, err = workspace.ScheduleMaintenance(ctx, tx, journal, installation, "owner",
				"early-workspace-update-campaign", "release", now.Add(time.Hour), now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		response := request(targetID, "update", "")
		var update operation.Operation
		if err := json.Unmarshal(response.Body.Bytes(), &update); response.Code != http.StatusAccepted || err != nil ||
			update.ID == "" || update.Type != "update" || update.WorkspaceID == nil || *update.WorkspaceID != targetID {
			t.Fatalf("workspace update before the deadline returned %d %s (%v), want a durable workspace update", response.Code, response.Body, err)
		}
		if persisted, err := journal.Get(ctx, update.ID); err != nil || persisted.Actor != "member" {
			t.Errorf("early update recorded actor %q (%v), want the requesting member", persisted.Actor, err)
		}
		var operationID, priorPower string
		if err := db.QueryRowContext(ctx, `SELECT operation_id,prior_desired_state FROM maintenance_targets
			WHERE campaign_id=$1 AND workspace_id=$2`, campaign.ID, targetID).Scan(&operationID, &priorPower); err != nil ||
			operationID != update.ID || priorPower != "stopped" {
			t.Errorf("scheduled target has operation %q and prior power %q (%v), want %q and stopped", operationID, priorPower, err, update.ID)
		}
		replay := request(targetID, "update", "")
		var replayed operation.Operation
		if err := json.Unmarshal(replay.Body.Bytes(), &replayed); replay.Code != http.StatusAccepted || err != nil || replayed.ID != update.ID {
			t.Errorf("early update replay returned %d and operation %q (%v), want %d and %q", replay.Code, replayed.ID, err, http.StatusAccepted, update.ID)
		}
		var updateCount int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM operations WHERE workspace_id=$1 AND type='update'", targetID).
			Scan(&updateCount); err != nil || updateCount != 1 {
			t.Errorf("early update and replay recorded %d operations (%v), want 1", updateCount, err)
		}
		if err := workspace.NewStore(db).PrepareMaintenanceTargets(ctx, journal, installation, campaign.ID, now.Add(30*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var otherOperation sql.NullString
		var otherState string
		if err := db.QueryRowContext(ctx, `SELECT operation_id,state FROM maintenance_targets WHERE campaign_id=$1 AND workspace_id=$2`,
			campaign.ID, otherID).Scan(&otherOperation, &otherState); err != nil || otherOperation.Valid || otherState != "pending" {
			t.Errorf("unrequested target has operation %v and state %q (%v), want no operation and pending", otherOperation, otherState, err)
		}
	})
}

func seedStoppedAPIWorkspace(t *testing.T, db *sql.DB, templateID, workspaceID string) {
	t.Helper()
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
		platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		SELECT $1,id,'member',$2,1,'artifact','https://github.com/example/repo','devcontainer.json',2,1024,40,'{"mode":"shared"}',
		'stopped','stopped',1,'fixture',$3,created_at,updated_at,1 FROM installations`, workspaceID, templateID, "fixture-"+workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,
		disk_state,reserved_at,cpu_memory_released_at) VALUES($1,2,1024,40,'released','reserved','now','now')`, workspaceID); err != nil {
		t.Fatal(err)
	}
}

func seedWorkspaceLaunch(t *testing.T, db *sql.DB, templateID, token string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
			 VALUES('00000000-0000-4000-8000-000000000001','active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release','now','now',1)`,
			`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version) VALUES
			('owner','owner@example.test','administrator','active','local','setup','now','now',1),
			('member','member@example.test','member','active','local','setup','now','now',1)`,
			`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
			VALUES('artifact','release','amd64','test','release','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available')`,
			`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES('profile','Small',2,1024,40,1,1)`,
			`INSERT INTO capacity_budgets(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
			  SELECT id,2,1024,40,'owner','now',1 FROM installations`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		// Workspace fixtures inherit the controller's real launch timestamp so
		// lifecycle and inactivity APIs exercise the same clock as production.
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET created_at=$1,updated_at=$1", now.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
			VALUES($1,'owner','Project','active',1,'now','now',1)`, templateID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO template_revisions(template_id,revision,repository_url,devcontainer_path,resource_profile_id,platform_artifact_id,secret_names_json,created_by,created_at)
			VALUES($1,1,'https://github.com/example/repo','.devcontainer/devcontainer.json','profile','artifact','[]','owner','now')`, templateID); err != nil {
			return err
		}
		return auth.NewStore(db).SaveSession(ctx, tx, auth.Session{ID: "launch-session", UserID: "member",
			TokenDigest: auth.TokenDigest(token), CSRFDigest: auth.TokenDigest(auth.CSRFToken(token)),
			Mode: "normal", AuthenticatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
	})
	if err != nil {
		t.Fatal(err)
	}
}
