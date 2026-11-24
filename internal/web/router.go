package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// NewManagementRouter registers exactly the reviewed API catalog. Construction
// fails until every operation is wired; an incomplete controller must not appear
// ready. The separate session handler preserves revoke-before-close semantics.
// The caller mounts this only on the management HTTPS origin, never a guest host.
func NewManagementRouter(session *SessionHandler, handlers map[string]AuthorizedHandler, githubCallback http.Handler) (http.Handler, error) {
	return newManagementRouter(ManagementRoutes(), session, handlers, githubCallback)
}

// NewActiveManagementRouter requires all active management operations.
// Bootstrap APIs are served separately by the protected local setup listener.
func NewActiveManagementRouter(session *SessionHandler, handlers map[string]AuthorizedHandler, githubCallback http.Handler) (http.Handler, error) {
	routes := slices.DeleteFunc(ManagementRoutes(), func(route ManagementRoute) bool {
		return strings.HasPrefix(route.Path, "/setup/") || strings.HasPrefix(route.Path, "/recovery/")
	})
	return newManagementRouter(routes, session, handlers, githubCallback)
}

func newManagementRouter(routes []ManagementRoute, session *SessionHandler, handlers map[string]AuthorizedHandler, githubCallback http.Handler) (http.Handler, error) {
	if session == nil || session.Origin == "" || session.Now == nil || session.Lookup == nil || session.Revoke == nil {
		return nil, errors.New("management session handler is incomplete")
	}
	known := make(map[string]bool, len(routes))
	for _, route := range routes {
		if route.OperationID == "getSession" || route.OperationID == "logout" {
			continue
		}
		if route.OperationID == "returnGitHubAppRegistration" {
			continue
		}
		if route.OperationID == "completeGitAuthorization" {
			if githubCallback == nil {
				return nil, errors.New("GitHub authorization callback is incompletely wired")
			}
			continue
		}
		known[route.OperationID] = true
		handler, ok := handlers[route.OperationID]
		if !ok || handler.DB == nil || handler.Now == nil || handler.Resolve == nil || handler.Apply == nil || handler.Origin != session.Origin {
			return nil, fmt.Errorf("management operation %s is incompletely wired", route.OperationID)
		}
	}
	for id := range handlers {
		if !known[id] {
			return nil, fmt.Errorf("unreviewed management operation %s", id)
		}
	}
	mux := http.NewServeMux()
	// Copy supplied handlers so later composition changes cannot change the
	// registered permission declaration or replace callbacks on a running router.
	sessionCopy := *session
	for _, route := range routes {
		pattern := route.Method + " /api/v1" + route.Path
		if route.OperationID == "getSession" || route.OperationID == "logout" {
			mux.Handle(pattern, &sessionCopy)
			continue
		}
		if route.OperationID == "completeGitAuthorization" {
			mux.Handle(pattern, githubCallback)
			continue
		}
		if route.OperationID == "returnGitHubAppRegistration" {
			mux.Handle(pattern, GitHubAppReturn(session.Origin))
			continue
		}
		handler := handlers[route.OperationID]
		handler.Route = route
		mux.Handle(pattern, &handler)
	}
	return WithRequestID(mux), nil
}
