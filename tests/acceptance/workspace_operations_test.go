package acceptance_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestWorkspaceOperationsAcceptance exercises admission and authority across
// the same retained workspace, checking the journal and reservations after
// rejected requests and after authorized maintenance and deletion.
func TestWorkspaceOperationsAcceptance(t *testing.T) {
	const origin = "https://management.test"
	ctx, now := t.Context(), time.Now().UTC()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "operations.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1,
		OwnershipMarker: "fixture-owner"}
	machines := fake.New()
	vm, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: "fixture-create", NetworkMode: "nat",
		ArtifactID: "fixture-image", Release: "fixture", Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 40 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	ref = vm.Ref
	admin, owner := uuid.NewString(), uuid.NewString()
	seedRecoveryRestart(t, db, ref, owner, now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin@example.test','administrator','active','local','setup',$2,$2,1)`, admin, now.Format(time.RFC3339Nano))
	exec(`UPDATE users SET role='member' WHERE id=$1`, owner)
	exec(`UPDATE workspaces SET observed_state='stopped' WHERE id=$1`, ref.WorkspaceID)
	exec(`INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		VALUES($1,2,1024,40,'released','reserved',$2,$2)`, ref.WorkspaceID, now.Format(time.RFC3339Nano))
	if err := workspace.NewStore(db).SetCapacityBudget(ctx, ref.InstallationID, workspace.Allocation{CPU: 1, MemoryMiB: 2048, DiskMiB: 80}, 0, admin, now); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(db)
	login := func(user string) (auth.Session, string) {
		t.Helper()
		session, token, err := auth.NewSession(uuid.NewString(), user, "normal", now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return sessions.SaveSession(ctx, tx, session)
		}); err != nil {
			t.Fatal(err)
		}
		return session, token
	}
	_, adminToken := login(admin)
	ownerSession, ownerToken := login(owner)
	clock := time.Now
	journal, registry := operation.NewJournal(db, clock, uuid.NewString), &proxy.Registry{}
	power := web.WorkspacePowerHandler(db, origin, clock, journal, registry)
	_, maintenance := web.MaintenanceHandlers(db, origin, clock, journal)
	mux := http.NewServeMux()
	for _, handler := range []*web.AuthorizedHandler{&power, &maintenance} {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, handler)
	}
	post := func(token, path, body, key, version string, wantStatus int, wantCode string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var problem web.Problem
		if w.Code != wantStatus || wantCode != "" && (json.Unmarshal(w.Body.Bytes(), &problem) != nil || problem.Code != wantCode) {
			t.Fatalf("POST %s = %d %s, want %d %s", path, w.Code, w.Body.String(), wantStatus, wantCode)
		}
		return w
	}
	actions := "/workspaces/" + ref.WorkspaceID + "/actions/"
	const initialVersion = `"1"`
	refusal := post(ownerToken, actions+"start", "{}", "acceptance-refused-start", initialVersion, 409, "workspace.capacity_exceeded")
	var problem web.Problem
	if err := json.Unmarshal(refusal.Body.Bytes(), &problem); err != nil || problem.Fields["resource"] != "cpu" {
		t.Errorf("capacity refusal points to %q (%v), want cpu", problem.Fields["resource"], err)
	}
	post(ownerToken, actions+"delete", "{}", "acceptance-denied-delete", initialVersion, 400, "workspace.confirm_data_loss")
	campaign := fmt.Sprintf(`{"releaseId":"fixture","deadline":%q}`, now.Add(time.Hour).Format(time.RFC3339Nano))
	post(ownerToken, "/admin/maintenance", campaign, "acceptance-denied-maintenance", "", 403, "")
	assertRecoveryEvidence(t, db, "refusal neither queued work nor spent capacity", `SELECT NOT EXISTS(SELECT 1 FROM operations)
		AND EXISTS(SELECT 1 FROM reservations WHERE workspace_id=$1 AND cpu_memory_state='released' AND disk_state='reserved')`, ref.WorkspaceID)

	closed := false
	unregister, err := registry.Register(proxy.Scope{UserID: owner, SessionID: ownerSession.ID, WorkspaceID: ref.WorkspaceID, IDE: true},
		func() error { return nil }, func() error { closed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	if err := sessions.RevokeAndClose(ctx, ownerSession.ID, now, "logout", registry); err != nil || !closed || registry.HasIDEConnection(ref.WorkspaceID) {
		t.Fatalf("revoke and close returned %v and close callback=%t, want synchronous closure", err, closed)
	}
	post(ownerToken, actions+"start", "{}", "acceptance-revoked-start", initialVersion, 401, "auth.required")
	post(adminToken, "/admin/maintenance", campaign, "acceptance-admin-maintenance", "", 202, "")
	assertRecoveryEvidence(t, db, "administrator scheduled maintenance for the same generation", `SELECT EXISTS(
		SELECT 1 FROM maintenance_campaigns c JOIN maintenance_targets t ON t.campaign_id=c.operation_id
		WHERE c.created_by=$1 AND c.state='scheduled' AND t.workspace_id=$2 AND t.generation=1 AND t.operation_id IS NULL)`, admin, ref.WorkspaceID)
	exec(`UPDATE capacity_budgets SET cpu_limit=2`)
	exec(`INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,1,'revoked')`, ref.WorkspaceID)
	started := post(adminToken, actions+"start", "{}", "acceptance-recovery-attempt", initialVersion, 202, "")
	var recovery operation.Operation
	if err := json.Unmarshal(started.Body.Bytes(), &recovery); err != nil || recovery.ID == "" {
		t.Fatalf("read admitted start = %+v (%v), want start operation", recovery, err)
	}
	worker := workspace.NewWorker(journal, nil, workspace.NewPowerExecutor(workspace.NewStore(db), machines, clock, nil))
	if err := worker.RunOnce(ctx, recovery.ID); err != nil {
		t.Fatal(err)
	}
	assertRecoveryEvidence(t, db, "revoked guest recovery failed without issuing a grant", `SELECT EXISTS(
		SELECT 1 FROM operations WHERE id=$1 AND status='failed') AND NOT EXISTS(SELECT 1 FROM enrollment_grants WHERE workspace_id=$2)`, recovery.ID, ref.WorkspaceID)
	if retained, err := machines.Inspect(ctx, ref); err != nil || retained.Power != provider.Stopped || retained.Ref != ref {
		t.Fatalf("provider after revoked recovery = %s/%+v (%v), want stopped/%+v", retained.Power, retained.Ref, err, ref)
	}
	post(adminToken, actions+"delete", `{"confirmDataLoss":true}`, "acceptance-confirmed-delete", `"2"`, 202, "")
	assertRecoveryEvidence(t, db, "confirmed deletion records authority and retains disk until cleanup", `SELECT EXISTS(
		SELECT 1 FROM workspaces w JOIN reservations r ON r.workspace_id=w.id JOIN audit_events a ON a.resource_id=w.id
		WHERE w.id=$1 AND w.desired_state='deleted' AND w.observed_state='stopped' AND w.deletion_commit_at IS NOT NULL
		AND r.disk_state='reserved' AND a.actor_id=$2 AND a.action='workspace.delete' AND a.reason_code='confirmed_data_loss')`, ref.WorkspaceID, admin)
}
