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
		SELECT '` + artifactID + `','template-api-release',architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,'template-api.utm',imported_by,imported_at,state FROM platform_artifacts WHERE id='artifact'`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	list, create, revise := web.TemplateHandlers(db, origin, func() time.Time { return now }, uuid.NewString)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/templates", &list)
	mux.Handle("POST /api/v1/templates", &create)
	mux.Handle("POST /api/v1/templates/{templateId}/revisions", &revise)
	request := func(method, path, payload, key, version string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, origin+"/api/v1/templates"+path, strings.NewReader(payload))
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, w.Code, w.Body, want)
		}
		return w
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
		Capabilities: provider.Capabilities{Create: true, Start: true, Architecture: "amd64", NetworkModes: []string{"shared"},
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
}
