package contract_test

import (
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
	"github.com/jflatten/ShiftBox/internal/web"
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
	const body = `{"templateId":"` + templateID + `","templateRevision":1}`
	token := strings.Repeat("A", 43)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	seedWorkspaceLaunch(t, db, templateID, token, now)
	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	handler := web.WorkspaceLaunchHandler(db, origin, clock, journal, web.WorkspaceLaunchPolicy{
		Capabilities: provider.Capabilities{Create: true, Start: true, Architecture: "amd64", NetworkModes: []string{"shared"},
			Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 1 << 20},
			Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 << 20, DiskBytes: 100 << 20}},
		Protocols: []string{"1"},
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
	accepted := request(body, key, csrf)
	check(accepted, 202, "")
	var op operation.Operation
	if err := json.Unmarshal(accepted.Body.Bytes(), &op); err != nil || op.WorkspaceID == nil || op.ID == "" {
		t.Fatalf("launch response = %+v, %v, want durable operation", op, err)
	}
	replayed := request(body, key, csrf)
	check(replayed, 202, "")
	if replayed.Body.String() != accepted.Body.String() {
		t.Error("retry returned a different operation")
	}
	check(request(withRetention, key, csrf), 409, "workspace.launch_conflict")
	check(request(body, "workspace-launch-key-two", csrf), 409, "workspace.capacity_exceeded")
	if _, err := db.Exec("UPDATE resource_profiles SET enabled=0 WHERE id='profile'"); err != nil {
		t.Fatal(err)
	}
	check(request(body, key, csrf), 202, "")
	check(request(body, "workspace-launch-key-two", csrf), 409, "workspace.template_unavailable")
	if _, err := db.Exec("UPDATE template_grants SET revoked_at='now' WHERE template_id=$1", templateID); err != nil {
		t.Fatal(err)
	}
	check(request(body, "workspace-launch-key-tri", csrf), 403, "auth.forbidden")
	check(request(body, key, csrf), 403, "auth.forbidden")
	var owner string
	var reservations, operations int
	if err := db.QueryRow(`SELECT owner_user_id,(SELECT COUNT(*) FROM reservations),(SELECT COUNT(*) FROM operations)
		FROM workspaces WHERE id=$1`, *op.WorkspaceID).Scan(&owner, &reservations, &operations); err != nil ||
		owner != "member" || reservations != 1 || operations != 1 {
		t.Fatalf("committed launch = %s, %d reservations, %d operations, %v, want member/1/1", owner, reservations, operations, err)
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
			VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available')`,
			`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES('profile','Small',2,1024,40,1,1)`,
			`INSERT INTO capacity_budgets(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
			  SELECT id,2,1024,40,'owner','now',1 FROM installations`,
		} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
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
