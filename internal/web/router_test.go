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
		if route.OperationID == "completeGitAuthorization" {
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
		if route.OperationID == "getSession" || route.OperationID == "logout" || route.OperationID == "completeGitAuthorization" {
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
