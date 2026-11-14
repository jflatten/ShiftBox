package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/identity"
)

// GitAuthorizationHandlers binds management operations to the signed-in user.
func GitAuthorizationHandlers(db *sql.DB, origin string, now func() time.Time, github *identity.GitHub) map[string]AuthorizedHandler {
	handlers := make(map[string]AuthorizedHandler, 3)
	if github == nil {
		return handlers
	}
	for _, route := range ManagementRoutes() {
		switch route.OperationID {
		case "getGitAuthorization", "beginGitAuthorization", "revokeGitAuthorization":
			handlers[route.OperationID] = AuthorizedHandler{
				DB: db, Origin: origin, Now: now, Route: route,
				Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
					return AuthorizationTarget{Action: "git.manage", Resource: auth.Resource{Type: "user", ID: principal.User.ID}}, nil
				},
				Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
					var result any
					var err error
					switch route.Method {
					case http.MethodGet:
						result, err = github.AuthorizationViewInTx(ctx, tx, principal.User.ID)
					case http.MethodPost:
						result, err = github.BeginAuthorizationInTx(ctx, tx, principal.User.ID, origin)
					case http.MethodDelete:
						err = github.RevokeAuthorizationInTx(ctx, tx, principal.User.ID)
					}
					if err != nil {
						return nil, gitProblem(err)
					}
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if route.Method == http.MethodDelete {
							w.WriteHeader(http.StatusNoContent)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(result)
					}), nil
				},
			}
		}
	}
	return handlers
}

// GitAuthorizationCallback handles the OAuth redirect using one-use state
// authority. It is restricted to the configured management HTTPS host.
func GitAuthorizationCallback(origin string, github *identity.GitHub) http.Handler {
	if github == nil {
		return nil
	}
	return gitAuthorizationCallback(origin, github.CompleteAuthorization)
}

func gitAuthorizationCallback(origin string, complete func(context.Context, string, string, string) error) http.Handler {
	configured, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 405, Code: "request.method_not_allowed", Title: "Method not allowed"})
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if configured == nil || configured.Scheme != "https" || r.TLS == nil || r.Host != configured.Host ||
			err != nil || len(query["state"]) != 1 || len(query["code"]) != 1 || query.Has("error") {
			WriteProblem(w, RequestID(r.Context()), gitProblem(identity.ErrGitHubState))
			return
		}
		if err := complete(r.Context(), query.Get("state"), query.Get("code"), origin); err != nil {
			WriteProblem(w, RequestID(r.Context()), gitProblem(err))
			return
		}
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
	})
}

func gitProblem(err error) error {
	switch {
	case errors.Is(err, identity.ErrGitHubState):
		err = Problem{Status: 400, Code: "git.invalid_state", Title: "GitHub authorization is no longer valid"}
	case errors.Is(err, identity.ErrGitHubIdentityConflict):
		err = Problem{Status: 409, Code: "git.identity_conflict", Title: "Use the GitHub account already linked to ShiftBox"}
	case errors.Is(err, identity.ErrGitHubReconnect):
		err = Problem{Status: 409, Code: "git.reconnect_required", Title: "Reconnect your GitHub account"}
	}
	return err
}
