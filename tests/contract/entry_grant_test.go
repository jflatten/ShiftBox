package contract_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestWorkspaceEntryGrantAdmission checks the exact destination and that a
// member may mint a grant for their own running workspace only.
func TestWorkspaceEntryGrantAdmission(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "entry.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const workspace = "00000000-0000-4000-8000-000000000004"
	const other = "00000000-0000-4000-8000-000000000005"
	token := strings.Repeat("A", 43)
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, "00000000-0000-4000-8000-000000000003", token, now)
	for id, owner := range map[string]string{workspace: "member", other: "owner"} {
		if _, err := db.Exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
			generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			SELECT $1,id,$2,'template',1,'artifact','https://github.com/example/repo','devcontainer.json',1,512,100,'{}',
			'running','running',1,$1,$1,'now','now',1 FROM installations`, id, owner); err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := config.NewServiceHosts(origin, "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	handler := web.WorkspaceEntryGrantHandler(db, origin, func() time.Time { return now }, hosts)
	request := func(id, service, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces/"+id+"/entry-grants", strings.NewReader(`{"service":"`+service+`"}`))
		r.SetPathValue("workspaceId", id)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("entry grant for %s/%s = %d %s, want %d", id, service, w.Code, w.Body, want)
		}
		return w
	}
	csrf := auth.CSRFToken(token)
	request(workspace, "preview-8080", "invalid", 403)
	request(other, "preview-8080", csrf, 403)
	request(workspace, "ide", csrf, 403)
	result := request(workspace, "preview-8080", csrf, 201)
	var grant struct {
		ExchangeURL string    `json:"exchangeUrl"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &grant); err != nil ||
		!strings.HasPrefix(grant.ExchangeURL, "https://preview-8080--"+workspace+".preview.test/_shiftbox/entry?grant=") ||
		!grant.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Error("response did not bind a one-minute grant to the exact service origin")
	}
}
