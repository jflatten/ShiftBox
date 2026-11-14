package contract_test

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
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestAdminHistoryDoesNotChangeActiveState verifies a maintenance announcement
// can disappear from a user's history without hiding the active certificate
// condition or removing maintenance, workspace provenance, and access grants.
func TestAdminHistoryDoesNotChangeActiveState(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "admin.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const installation = "00000000-0000-4000-8000-000000000001"
	now := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	deadline := now.Add(24 * time.Hour)
	memberToken, templateID, workspaceID := strings.Repeat("A", 43), uuid.NewString(), uuid.NewString()
	seedWorkspaceLaunch(t, db, templateID, memberToken, now)
	adminSession, adminToken, err := auth.NewSession(uuid.NewString(), "owner", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
		repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
		generation,ownership_marker,created_at,updated_at,version)
		VALUES($1,$2,'member',$3,1,'artifact','https://github.com/example/repo','devcontainer.json',
		2,1024,40,'{}','stopped','stopped',1,'owned','now','now',1)`, workspaceID, installation, templateID)
	exec(`INSERT INTO protected_values(id,value_json) VALUES('certificate-fixture','{}')`)
	exec(`INSERT INTO browser_certificates(id,chain_pem,private_key_ciphertext_id,not_before,not_after,fingerprint_sha256,state)
		VALUES('certificate-fixture','fixture-chain','certificate-fixture',$1,$2,'fixture','active')`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(10*time.Minute).Format(time.RFC3339Nano))
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := auth.NewStore(db).SaveSession(ctx, tx, adminSession); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
			VALUES('connection-grant','owner','workspace',$1,'workspace.connect_admin','owner',$2)`, workspaceID, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: "owner",
			Action: "workspace.connect_admin", ResourceType: "workspace", ResourceID: workspaceID,
			Outcome: "allowed", ReasonCode: "explicit_grant", Metadata: audit.Metadata{GrantID: "connection-grant"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	routes := func() *http.ServeMux {
		publish, notices, receipt := web.NoticeHandlers(db, origin, clock, uuid.NewString)
		campaigns, schedule := web.MaintenanceHandlers(db, origin, clock, journal)
		_, workspace := web.WorkspaceReadHandlers(db, origin, clock, nil)
		infrastructure := web.InfrastructureStatusHandler(db, origin, clock, provider.Capabilities{
			Name: "utm", Version: "5.0.8", Architecture: "amd64", NetworkModes: []string{"shared"},
		})
		importArtifact := app.NewPlatformImport(db, journal, origin, installation, clock, nil).Handler()
		mux := http.NewServeMux()
		for _, h := range []*web.AuthorizedHandler{&publish, &notices, &receipt, &campaigns, &schedule, &workspace, &infrastructure, &importArtifact} {
			mux.Handle(h.Route.Method+" /api/v1"+h.Route.Path, h)
		}
		return mux
	}
	mux := routes()
	send := func(token, method, path, data, key string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+"/api/v1"+path, strings.NewReader(data))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s returned %d %s, want %d", method, path, w.Code, w.Body, want)
		}
		return w
	}
	campaign := send(adminToken, "POST", "/admin/maintenance",
		fmt.Sprintf(`{"releaseId":"release","deadline":%q}`, deadline.Format(time.RFC3339Nano)), "schedule-fixture-maintenance", http.StatusAccepted)
	var accepted operation.Operation
	if err := json.Unmarshal(campaign.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("accepted campaign = %+v (%v), want the scheduled operation", accepted, err)
	}
	expiration := now.Add(5 * time.Minute)
	published := send(adminToken, "POST", "/admin/notices",
		fmt.Sprintf(`{"title":"Maintenance tonight","body":"Push work you need to keep.","severity":"warning","expiresAt":%q}`, expiration.Format(time.RFC3339Nano)), "", http.StatusCreated)
	var notice struct{ ID string }
	if err := json.Unmarshal(published.Body.Bytes(), &notice); err != nil || notice.ID == "" {
		t.Fatalf("published notice = %+v (%v), want notice ID", notice, err)
	}
	receipt := "/notices/" + notice.ID + "/receipt"
	send(memberToken, "PATCH", receipt, `{"state":"read"}`, "", http.StatusNoContent)
	send(memberToken, "PATCH", receipt, `{"state":"dismissed"}`, "", http.StatusNoContent)

	// Rebuild the HTTP handlers as the native controller does on restart.
	mux = routes()
	now = now.Add(20 * time.Minute)
	history := send(memberToken, "GET", "/notices?includeDismissed=true", "", "", http.StatusOK)
	if !strings.Contains(history.Body.String(), notice.ID) {
		t.Errorf("dismissed notice history = %s, want the expired notice until its receipt is pruned", history.Body)
	}
	cutoff := now.Add(-time.Minute)
	if got, err := app.PruneDismissedNotices(ctx, db, now, cutoff); err != nil || got != 1 {
		t.Fatalf("pruned notice receipts = %d (%v), want 1", got, err)
	}
	if got, err := audit.Prune(ctx, db, cutoff); err != nil || got == 0 {
		t.Fatalf("pruned audit records = %d (%v), want old history removed", got, err)
	}
	history = send(memberToken, "GET", "/notices?includeDismissed=true", "", "", http.StatusOK)
	if strings.Contains(history.Body.String(), notice.ID) {
		t.Errorf("pruned notice still appears in user history: %s", history.Body)
	}
	status := send(adminToken, "GET", "/admin/infrastructure", "", "", http.StatusOK)
	var infrastructure struct {
		Conditions []struct {
			Key, Severity string
			Active        bool
		}
	}
	if err := json.Unmarshal(status.Body.Bytes(), &infrastructure); err != nil || len(infrastructure.Conditions) != 1 ||
		infrastructure.Conditions[0].Key != "browser_certificate.invalid" || infrastructure.Conditions[0].Severity != "critical" ||
		!infrastructure.Conditions[0].Active {
		t.Errorf("certificate conditions after history cleanup = %+v (%v), want active critical certificate condition", infrastructure, err)
	}
	detail := send(memberToken, "GET", "/workspaces/"+workspaceID, "", "", http.StatusOK)
	var workspace struct {
		OwnerID, TemplateID, PlatformRelease string
		TemplateRevision                     int
		Maintenance                          *struct {
			State, ReleaseID string
			Deadline         time.Time
		}
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &workspace); err != nil || workspace.OwnerID != "member" ||
		workspace.TemplateID != templateID || workspace.TemplateRevision != 1 || workspace.PlatformRelease != "release" ||
		workspace.Maintenance == nil || workspace.Maintenance.State != "pending" ||
		workspace.Maintenance.ReleaseID != "release" || !workspace.Maintenance.Deadline.Equal(deadline) {
		t.Errorf("workspace after history cleanup = %+v (%v), want original owner, provenance, and pending maintenance", workspace, err)
	}
	var retained bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_grants WHERE id='connection-grant' AND revoked_at IS NULL)
		AND EXISTS(SELECT 1 FROM operations WHERE id=$1 AND status='pending')`, accepted.ID).Scan(&retained); err != nil || !retained {
		t.Errorf("grant and pending operation survived cleanup = %t (%v), want true", retained, err)
	}

	importBody := fmt.Sprintf(`{"folder":%q,"makeDefault":true}`, t.TempDir())
	const importKey = "import-admin-operations-fixture"
	send(memberToken, "POST", "/admin/platform-artifacts", importBody, importKey, http.StatusForbidden)
	artifact := send(adminToken, "POST", "/admin/platform-artifacts", importBody, importKey, http.StatusAccepted)
	var importOp operation.Operation
	if err := json.Unmarshal(artifact.Body.Bytes(), &importOp); err != nil || importOp.Status != "pending" || importOp.ID == "" {
		t.Fatalf("artifact import = %+v (%v), want pending operation", importOp, err)
	}
}
