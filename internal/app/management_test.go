package app

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/secret"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestManagementServices maps built handlers to the exact catalog entries
// and keeps the completeness check for a production router.
func TestManagementServices(t *testing.T) {
	db := &sql.DB{}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	settings := config.InstallationSettings{
		ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test",
		PreviewHostPattern: "{service}--{workspace}.preview.test",
	}
	state := &ControllerState{DB: db, Installation: ActiveInstallation{Settings: settings}, Secrets: secret.NewService(cipher)}
	services, err := NewManagementServices(state, operation.NewJournal(db, time.Now, uuid.NewString), &proxy.Registry{}, web.WorkspaceLaunchPolicy{}, nil, identity.PasswordAdmin{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	catalog := make(map[string]web.ManagementRoute)
	for _, route := range web.ManagementRoutes() {
		catalog[route.OperationID] = route
	}
	for _, id := range []string{"listWorkspaces", "getWorkspace", "launchWorkspace", "requestWorkspaceAction",
		"createWorkspaceEntryGrant", "listWorkspaceConnectionGrants", "revokeWorkspaceConnectionGrant",
		"getOperation", "streamOperationEvents", "listTemplates", "listTemplateCatalog", "createTemplate",
		"createTemplateRevision", "updateTemplate", "grantTemplateLaunch", "revokeTemplateLaunch",
		"updateCapacityBudget", "createResourceProfile", "getInfrastructureStatus", "replaceBrowserCertificate",
		"listSecretMetadata", "setSecret", "deleteSecretOverride", "listUsers", "changeOwnPassword", "createLocalUser", "resetLocalUserPassword",
		"getGitAuthorization", "beginGitAuthorization", "revokeGitAuthorization", "listMaintenanceCampaigns", "createMaintenanceCampaign"} {
		handler, ok := services.Handlers[id]
		if !ok || handler.Route != catalog[id] || handler.DB != db || handler.Origin != "https://management.test" {
			t.Errorf("handler for %s = route %+v, want reviewed catalog route %+v", id, handler.Route, catalog[id])
		}
	}
	if _, err := web.NewActiveManagementRouter(services.Session, services.Handlers, services.GitHubCallback); err == nil {
		t.Fatal("management API accepted a partial catalog")
	}
}

// TestManagementSiteRoutes keeps the API and identity paths outside the static
// file handler and confines the setup page to the local setup listener.
func TestManagementSiteRoutes(t *testing.T) {
	static, err := web.NewStaticHandler(fstest.MapFS{
		"index.html": {Data: []byte("home")}, "setup/index.html": {Data: []byte("local setup")},
		"setup/index.txt": {Data: []byte("setup route payload")},
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Handler", name) })
	}
	site, err := NewManagementSite(marker("api"), marker("login"), marker("dex"), static)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, handler string
		status        int
	}{
		{"/", "", 200}, {"/api/v1/session", "api", 200}, {"/auth/callback", "login", 200},
		{"/dex/approval", "dex", 200}, {"/setup/", "", 404}, {"/setup/index.txt", "", 404},
		{"/internal/v1/guest/poll", "", 404},
	} {
		w := httptest.NewRecorder()
		site.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://management.test"+tc.path, nil))
		if w.Code != tc.status || w.Header().Get("X-Handler") != tc.handler {
			t.Errorf("%s = (%d, %q), want (%d, %q)", tc.path, w.Code, w.Header().Get("X-Handler"), tc.status, tc.handler)
		}
	}
}
