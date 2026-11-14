package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
)

// AuthorizationTarget contains controller-derived resource state and grants.
// Resolve must read authoritative records; browser-provided ownership is not trusted.
type AuthorizationTarget struct {
	Action   string
	Resource auth.Resource
	Grants   []auth.ResourceGrant
}

// AuthorizedHandler keeps current authority, policy, required audit, and durable
// intent in one transaction. Apply returns a response handler that runs only
// after commit. Resolve, Audit, and Apply must not perform external effects.
// Streaming responses must continue to recheck authority after admission.
type AuthorizedHandler struct {
	DB     *sql.DB
	Origin string
	Now    func() time.Time
	Route  ManagementRoute
	// Lookup defaults to the database store. Protected setup/recovery wiring can
	// supply a lookup that additionally proves its local/review eligibility gates.
	Lookup  func(context.Context, *sql.Tx, string) (auth.Principal, error)
	Resolve func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error)
	Audit   func(context.Context, *sql.Tx, auth.Principal, AuthorizationTarget) error
	Apply   func(context.Context, *sql.Tx, *http.Request, auth.Principal, AuthorizationTarget, auth.Decision) (http.Handler, error)
}

// ServeHTTP denies invalid authority before invoking the intent callback and
// withholds every success response until the intent and any required audit commit.
func (h *AuthorizedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != h.Route.Method {
		w.Header().Set("Allow", h.Route.Method)
		WriteProblem(w, RequestID(r.Context()), Problem{Status: 405, Code: "request.method_not_allowed", Title: "Method not allowed"})
		return
	}
	token, err := SessionToken(r)
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	if h.DB == nil || h.Now == nil || h.Resolve == nil || h.Apply == nil || (h.Route.Action == "") == (h.Route.Resolver == "") {
		WriteProblem(w, RequestID(r.Context()), errors.New("incomplete authorized route"))
		return
	}
	lookup := h.Lookup
	if lookup == nil {
		lookup = auth.NewStore(h.DB).Principal
	}
	var response http.Handler
	err = database.Transaction(r.Context(), h.DB, func(tx *sql.Tx) error {
		principal, err := lookup(r.Context(), tx, token)
		if errors.Is(err, sql.ErrNoRows) {
			return Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		if err != nil {
			return err
		}
		now := h.Now()
		if principal.Session.TokenDigest != auth.TokenDigest(token) || !auth.Authorize(principal, "session.view", auth.Resource{}, nil, now).Allowed {
			return Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		if err := checkMutation(r, h.Origin, principal.Session); err != nil {
			return err
		}
		target, err := h.Resolve(r.Context(), tx, r, principal)
		if err != nil {
			return err
		}
		if target.Action == "" || (h.Route.Action != "" && target.Action != h.Route.Action) {
			return errors.New("authorization resolver disagrees with route")
		}
		decision := auth.Authorize(principal, target.Action, target.Resource, target.Grants, now)
		if !decision.Allowed {
			return Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		if decision.AuditRequired {
			if h.Audit == nil {
				return errors.New("required access audit is unavailable")
			}
			if err := h.Audit(r.Context(), tx, principal, target); err != nil {
				return err
			}
		}
		response, err = h.Apply(r.Context(), tx, r, principal, target, decision)
		if err != nil {
			return err
		}
		if response == nil {
			return errors.New("authorized action returned no response")
		}
		return nil
	})
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	response.ServeHTTP(w, r)
}
