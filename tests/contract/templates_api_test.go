package contract_test

import (
	"database/sql"
	"encoding/json"
	"io"
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

// TestTemplateAPI exercises an owner creating and revising a template,
// including retries, stale versions, and attempts to edit another owner's project.
func TestTemplateAPI(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "template.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const privateTemplate = "00000000-0000-4000-8000-000000000003"
	const profileID = "00000000-0000-4000-8000-000000000004"
	const artifactID = "00000000-0000-4000-8000-000000000005"
	const body = `{"name":"Example","repositoryUrl":"https://github.com/example/repo","repositoryRef":"main","resourceProfileId":"` + profileID + `","platformArtifactId":"` + artifactID + `","secretNames":["API_KEY"]}`
	token := strings.Repeat("A", 43)
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, privateTemplate, token, now)
	for _, query := range []string{
		`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version) VALUES('` + profileID + `','Selected',2,1024,40,1,1)`,
		`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		SELECT '` + artifactID + `','template-api-release','arm64',platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,'template-api.utm',imported_by,imported_at,state FROM platform_artifacts WHERE id='artifact'`,
		`INSERT INTO resource_profiles(id,name,cpu_count,memory_mib,disk_mib,enabled,version)
		VALUES('disabled','Disabled profile',2,1024,40,0,1),('oversized','Oversized profile',100,1024,40,1,1)`,
		`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		SELECT 'other-protocol','Other protocol',architecture,platform_version,guest_release,'999',owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,'other.utm',imported_by,imported_at,state FROM platform_artifacts WHERE id='artifact'`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	list, create, revise := web.TemplateHandlers(db, origin, func() time.Time { return now }, uuid.NewString)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/templates", &list)
	catalog := web.TemplateCatalogHandler(db, origin, func() time.Time { return now }, web.WorkspaceLaunchPolicy{
		Capabilities: provider.Capabilities{Create: true, Start: true, Architecture: "amd64", Architectures: []string{"amd64", "arm64"},
			Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 1 << 20},
			Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 << 20, DiskBytes: 100 << 20}},
		Protocols: []string{"1"},
	})
	mux.Handle("GET /api/v1/templates/catalog", &catalog)
	mux.Handle("POST /api/v1/templates", &create)
	mux.Handle("POST /api/v1/templates/{templateId}/revisions", &revise)
	update := web.TemplateUpdateHandler(db, origin, func() time.Time { return now })
	grant, revoke := web.TemplateGrantHandlers(db, origin, func() time.Time { return now })
	mux.Handle("PATCH /api/v1/templates/{templateId}", &update)
	mux.Handle("POST /api/v1/templates/{templateId}/grants", &grant)
	mux.Handle("DELETE /api/v1/templates/{templateId}/grants/{userId}", &revoke)
	send := func(sessionToken, method, path, payload, key, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+"/api/v1/templates"+path, strings.NewReader(payload))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: sessionToken})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(sessionToken))
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, want)
		}
		return w
	}
	request := func(method, path, payload, key, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return send(token, method, path, payload, key, version, want)
	}
	if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET is_default=1 WHERE id=$1", artifactID); err != nil {
		t.Fatal(err)
	}
	// A member can choose available names without accessing infrastructure
	// metadata or seeing profiles and releases that cannot be launched.
	send("", "GET", "/catalog", "", "", "", 401)
	choices := request("GET", "/catalog", "", "", "", 200).Body.String()
	for _, want := range []string{`"defaultPlatformArtifactId":"` + artifactID + `"`, `"id":"artifact","name":"release"`, `"id":"` + profileID + `","name":"Selected"`, `"id":"` + artifactID + `","name":"template-api-release"`} {
		if !strings.Contains(choices, want) {
			t.Errorf("template catalog omitted %s: %s", want, choices)
		}
	}
	for _, excluded := range []string{"Disabled profile", "Oversized profile", "Other protocol", "manifest", "bundle"} {
		if strings.Contains(choices, excluded) {
			t.Errorf("template catalog exposed %s: %s", excluded, choices)
		}
	}
	const createKey = "create-template-fixture"
	const reviseKey = "revise-template-fixture"
	request("POST", "", strings.Replace(body, "github.com", "other.test", 1), createKey, "", 400)
	request("POST", "", strings.Replace(body, `"main"`, `"--detach"`, 1), createKey, "", 400)
	request("POST", "", strings.Replace(body, profileID, uuid.NewString(), 1), createKey, "", 409)
	request("POST", "", strings.TrimSuffix(body, "}")+`,"networkPolicyId":"`+uuid.NewString()+`"}`, createKey, "", 409)
	first := request("POST", "", body, createKey, "", 201)
	var template struct {
		ID              string
		OwnerID         string
		CurrentRevision int
		Version         int
	}
	if err := json.Unmarshal(first.Body.Bytes(), &template); err != nil || !web.ValidUUID(template.ID) ||
		template.OwnerID != "member" || template.CurrentRevision != 1 || template.Version != 1 {
		t.Fatalf("created template = %+v, %v, want member-owned revision 1", template, err)
	}
	request("POST", "/"+privateTemplate+"/revisions", body, reviseKey, `"1"`, 403)
	path := "/" + template.ID + "/revisions"
	secondBody := strings.Replace(body, `"main"`, `"next"`, 1)
	second := request("POST", path, secondBody, reviseKey, `"1"`, 201)
	if err := json.Unmarshal(second.Body.Bytes(), &template); err != nil || template.CurrentRevision != 2 || template.Version != 2 {
		t.Fatalf("revised template = %+v, %v, want revision 2 and version 2", template, err)
	}
	if replay := request("POST", path, secondBody, reviseKey, `"1"`, 201); replay.Body.String() != second.Body.String() {
		t.Fatal("revision replay changed the committed response")
	}
	if replay := request("POST", "", body, createKey, "", 201); replay.Body.String() != first.Body.String() {
		t.Fatal("creation replay changed the committed response")
	}
	request("POST", "", secondBody, createKey, "", 409)
	request("POST", path, body, "new-revision-fixture", `"1"`, 412)
	var firstRef string
	if err := db.QueryRowContext(ctx, "SELECT repository_ref FROM template_revisions WHERE template_id=$1 AND revision=1", template.ID).Scan(&firstRef); err != nil || firstRef != "main" {
		t.Fatalf("original repository ref = %q, %v, want main", firstRef, err)
	}
	shown := request("GET", "", "", "", "", 200).Body.String()
	if !strings.Contains(shown, template.ID) || strings.Contains(shown, privateTemplate) {
		t.Fatalf("member template list = %s, want only the member's template", shown)
	}
	clock := func() time.Time { return now }
	journal := operation.NewJournal(db, clock, uuid.NewString)
	launch := web.WorkspaceLaunchHandler(db, origin, clock, journal, web.WorkspaceLaunchPolicy{
		Capabilities: provider.Capabilities{Create: true, Start: true, Architecture: "amd64", Architectures: []string{"amd64", "arm64"}, NetworkModes: []string{"shared"},
			Minimum: provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 1 << 20},
			Maximum: provider.Resources{CPU: 8, MemoryBytes: 8192 << 20, DiskBytes: 100 << 20}},
		Protocols: []string{"1"},
	})
	req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces",
		strings.NewReader(`{"templateId":"`+template.ID+`","templateRevision":2}`))
	req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	req.Header.Set("Origin", origin)
	req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	req.Header.Set("Idempotency-Key", "launch-saved-template")
	w := httptest.NewRecorder()
	launch.ServeHTTP(w, req)
	var op operation.Operation
	if err := json.Unmarshal(w.Body.Bytes(), &op); w.Code != 202 || err != nil || op.WorkspaceID == nil {
		t.Fatalf("launch saved template = %d %s, want a durable workspace operation", w.Code, w.Body)
	}
	var snapshotMatches bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspaces WHERE id=$1 AND template_id=$2
		AND template_revision=2 AND repository_url='https://github.com/example/repo' AND repository_ref='next'
		AND secret_names_json='["API_KEY"]' AND owner_user_id='member' AND platform_artifact_id=$3
		AND devcontainer_path='.devcontainer/devcontainer.json' AND network_policy_snapshot_json='{"mode":"shared"}'
		AND cpu_count=2 AND memory_mib=1024 AND disk_mib=40)`, *op.WorkspaceID, template.ID, artifactID).Scan(&snapshotMatches)
	if err != nil || !snapshotMatches {
		t.Fatalf("snapshot matches saved revision and resolved configuration = %v, %v, want true", snapshotMatches, err)
	}
	projectPath := "/" + template.ID
	request("PATCH", "/"+privateTemplate, `{"name":"Unauthorized"}`, "", `"1"`, 403)
	request("PATCH", projectPath, `{"name":"Renamed"}`, "", `"1"`, 412)
	updated := request("PATCH", projectPath, `{"name":"Renamed","state":"archived"}`, "", `"2"`, 200)
	if updated.Header().Get("ETag") != `"3"` || !strings.Contains(updated.Body.String(), `"currentRevision":2`) {
		t.Fatal("archive changed the current revision or returned the wrong version")
	}
	if strings.Contains(request("GET", "", "", "", "", 200).Body.String(), template.ID) {
		t.Fatal("member list included an archived template")
	}
	request("GET", "?archived=true", "", "", "", 403)
	w = httptest.NewRecorder()
	nextLaunch := req.Clone(ctx)
	nextLaunch.Body = io.NopCloser(strings.NewReader(`{"templateId":"` + template.ID + `","templateRevision":2}`))
	nextLaunch.Header.Set("Idempotency-Key", "launch-archived-template")
	launch.ServeHTTP(w, nextLaunch)
	if w.Code != 403 {
		t.Fatalf("launch archived template = %d, want 403", w.Code)
	}
	request("PATCH", projectPath, `{"state":"active"}`, "", `"3"`, 200)
	grantee := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'grantee@example.test','member','active','local','setup','now','now',1)`, grantee); err != nil {
		t.Fatal(err)
	}
	const grantKey = "grant-launch-template"
	payload := `{"userId":"` + grantee + `","acknowledgeProjectSecretExposure":true}`
	request("POST", projectPath+"/grants", strings.Replace(payload, "true", "false", 1), grantKey, "", 400)
	request("POST", projectPath+"/grants", payload, grantKey, "", 204)
	request("POST", projectPath+"/grants", payload, grantKey, "", 204)
	signIn := func(user string) string {
		t.Helper()
		session, token, err := auth.NewSession(uuid.NewString(), user, "normal", now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return auth.NewStore(db).SaveSession(ctx, tx, session)
		}); err != nil {
			t.Fatal(err)
		}
		return token
	}
	granteeToken := signIn(grantee)
	ownerList := request("GET", "", "", "", "", 200).Body.String()
	// Display names come from authorized records, including a pinned image
	// that may no longer be available in the new-launch catalog.
	for _, field := range []string{`"ownerName":"member@example.test"`, `"resourceProfileName":"Selected"`, `"platformImageName":"template-api-release"`} {
		if !strings.Contains(ownerList, field) {
			t.Errorf("template list omitted %s: %s", field, ownerList)
		}
	}
	// Search is case-insensitive and literal: SQL wildcard characters must
	// not turn a narrow search into every template the caller can access.
	for _, scenario := range []struct {
		query string
		count int
	}{{"RENAMED", 1}, {"example/repo", 1}, {"no-match", 0}, {"%25", 0}, {"%5F", 0}} {
		response := request("GET", "?q="+scenario.query, "", "", "", 200)
		var page struct{ Items []json.RawMessage }
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != scenario.count {
			t.Errorf("template search %q returned %d items, want %d", scenario.query, len(page.Items), scenario.count)
		}
	}
	request("GET", "?q="+strings.Repeat("x", 201), "", "", "", 400)
	if !strings.Contains(ownerList, "grantee@example.test") || !strings.Contains(ownerList, grantee) {
		t.Fatal("template owner could not see the active launch grant")
	}
	granteeList := send(granteeToken, "GET", "", "", "", "", 200).Body.String()
	if strings.Contains(granteeList, "launchGrants") || strings.Contains(granteeList, "grantee@example.test") {
		t.Fatal("launch-only access exposed grant membership")
	}
	visible := func(sessionToken, filter string, want bool) {
		t.Helper()
		list := send(sessionToken, "GET", filter, "", "", "", 200)
		if got := strings.Contains(list.Body.String(), template.ID); got != want {
			t.Fatalf("template visibility = %t, want %t", got, want)
		}
	}
	visible(granteeToken, "", true)
	request("PATCH", projectPath, `{"state":"archived"}`, "", `"4"`, 200)
	visible(granteeToken, "", false)
	adminToken := signIn("owner")
	visible(adminToken, "?archived=true", true)
	var retained bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM template_grants WHERE template_id=$1 AND user_id=$2 AND revoked_at IS NULL)", template.ID, grantee).
		Scan(&retained); err != nil || !retained {
		t.Fatalf("launch grant retained on archive = %v, %v, want true", retained, err)
	}
	request("DELETE", projectPath+"/grants/"+grantee, "", "", "", 204)
	request("POST", projectPath+"/grants", payload, grantKey, "", 204)
	send(adminToken, "PATCH", projectPath, `{"state":"active"}`, "", `"5"`, 200)
	visible(granteeToken, "", false)
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM template_grants WHERE template_id=$1 AND user_id=$2 AND revoked_at IS NULL)", template.ID, grantee).
		Scan(&retained); err != nil || retained {
		t.Fatalf("launch grant restored by stale retry = %v, %v, want false", retained, err)
	}
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE resource_id=$1 AND action='template.share'`, template.ID).
		Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("template share audit count = %d, %v, want one grant and one revoke", auditCount, err)
	}
}
