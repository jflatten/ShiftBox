package web_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestManagementRouterRequiresCompleteWiring prevents missing or unknown API implementations from silently shipping.
func TestManagementRouterRequiresCompleteWiring(t *testing.T) {
	session, _, _ := sessionHandlerFixture(t)
	session.Revoke = func(context.Context, string) error { return nil }
	handlers := routerHandlers(t)
	if _, err := web.NewManagementRouter(session, handlers, nil); err == nil {
		t.Fatal("NewManagementRouter(missing GitHub callback) succeeded")
	}
	delete(handlers, "launchWorkspace")
	if _, err := web.NewManagementRouter(session, handlers, routerGitCallback()); err == nil {
		t.Fatal("NewManagementRouter(missing launch) succeeded")
	}
	handlers = routerHandlers(t)
	handlers["unreviewedOperation"] = handlers["launchWorkspace"]
	if _, err := web.NewManagementRouter(session, handlers, routerGitCallback()); err == nil {
		t.Fatal("NewManagementRouter(unknown operation) succeeded")
	}
	handlers = routerHandlers(t)
	h := handlers["launchWorkspace"]
	h.Apply = nil
	handlers["launchWorkspace"] = h
	if _, err := web.NewManagementRouter(session, handlers, routerGitCallback()); err == nil {
		t.Fatal("NewManagementRouter(missing intent callback) succeeded")
	}
}

// TestActiveManagementRouterKeepsSetupLocal prevents bootstrap APIs from
// reaching the active HTTPS origin while requiring normal routes.
func TestActiveManagementRouterKeepsSetupLocal(t *testing.T) {
	session, _, _ := sessionHandlerFixture(t)
	session.Revoke = func(context.Context, string) error { return nil }
	handlers := routerHandlers(t)
	if _, err := web.NewActiveManagementRouter(session, handlers, routerGitCallback()); err == nil {
		t.Fatal("active router accepted protected setup handlers")
	}
	for _, route := range web.ManagementRoutes() {
		if strings.HasPrefix(route.Path, "/setup/") || strings.HasPrefix(route.Path, "/recovery/") {
			delete(handlers, route.OperationID)
		}
	}
	router, err := web.NewActiveManagementRouter(session, handlers, routerGitCallback())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/setup/status", http.StatusNotFound},
		{"POST", "/setup/activate", http.StatusNotFound},
		{"PUT", "/setup/certificate", http.StatusNotFound},
		{"GET", "/recovery/fixture", http.StatusNotFound},
		{"PUT", "/session/password", http.StatusUnauthorized},
		{"POST", "/workspaces", http.StatusUnauthorized},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tc.method, "https://management.test/api/v1"+tc.path, nil))
		if response.Code != tc.status {
			t.Errorf("active router %s %s = %d, want %d", tc.method, tc.path, response.Code, tc.status)
		}
	}
	delete(handlers, "launchWorkspace")
	if _, err := web.NewActiveManagementRouter(session, handlers, routerGitCallback()); err == nil || !strings.Contains(err.Error(), "launchWorkspace") {
		t.Errorf("active router without workspace launch = %v, want incomplete launch wiring", err)
	}
}

// TestManagementRouterGuardsCatalogRoutes exercises real mux registration for every reviewed endpoint before resource resolution.
func TestManagementRouterGuardsCatalogRoutes(t *testing.T) {
	session, _, _ := sessionHandlerFixture(t)
	session.Revoke = func(context.Context, string) error { return nil }
	handlers := routerHandlers(t)
	// A caller cannot weaken a route's permission declaration or move its path.
	h := handlers["launchWorkspace"]
	h.Route = web.ManagementRoute{Method: "GET", Path: "/unreviewed", Action: "session.view"}
	handlers["launchWorkspace"] = h
	router, err := web.NewManagementRouter(session, handlers, routerGitCallback())
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range web.ManagementRoutes() {
		path := route.Path
		for _, parameter := range []string{"workspaceId", "operationId", "templateId", "userId", "noticeId", "reviewId", "scopeId"} {
			path = strings.ReplaceAll(path, "{"+parameter+"}", "00000000-0000-4000-8000-000000000001")
		}
		path = strings.NewReplacer("{action}", "start", "{scope}", "user", "{name}", "EXAMPLE").Replace(path)
		request := httptest.NewRequest(route.Method, "https://management.test/api/v1"+path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if route.OperationID == "completeGitAuthorization" || route.OperationID == "returnGitHubAppRegistration" {
			want = http.StatusBadRequest
		}
		if response.Code != want {
			t.Errorf("registered %s %s status = %d, want %d", route.Method, path, response.Code, want)
		}
		if response.Header().Get("X-Request-ID") == "" {
			t.Errorf("registered %s missing controller request ID", route.OperationID)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "https://management.test/unreviewed", nil))
	if response.Code != 404 {
		t.Errorf("unreviewed path status = %d, want 404", response.Code)
	}
}

func routerHandlers(t *testing.T) map[string]web.AuthorizedHandler {
	t.Helper()
	session, _, _ := sessionHandlerFixture(t)
	session.Revoke = func(context.Context, string) error { return nil }
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	handlers := make(map[string]web.AuthorizedHandler)
	for _, route := range web.ManagementRoutes() {
		if route.OperationID == "getSession" || route.OperationID == "logout" || route.OperationID == "completeGitAuthorization" ||
			route.OperationID == "returnGitHubAppRegistration" {
			continue
		}
		handlers[route.OperationID] = web.AuthorizedHandler{DB: db, Origin: session.Origin, Now: session.Now,
			Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
				t.Error("unauthenticated request reached resolver")
				return web.AuthorizationTarget{}, nil
			},
			Apply: func(context.Context, *sql.Tx, *http.Request, auth.Principal, web.AuthorizationTarget, auth.Decision) (http.Handler, error) {
				t.Error("unauthenticated request reached intent")
				return nil, nil
			},
		}
	}
	return handlers
}

func routerGitCallback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
}
