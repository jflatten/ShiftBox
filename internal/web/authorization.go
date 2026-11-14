package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// AuthorizationTarget contains controller-derived resource state and grants.
// Resolve must read authoritative records; browser-provided ownership is not trusted.
type AuthorizationTarget struct {
	Action   string
	Resource auth.Resource
	Grants   []auth.ResourceGrant
	launch   *workspaceLaunchRequest
	entry    *proxy.OriginScope
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
		if !h.Route.AcceptsAction(target.Action) {
			return errors.New("authorization resolver disagrees with route")
		}
		decision := auth.Authorize(principal, target.Action, target.Resource, target.Grants, now)
		if !decision.Allowed {
			return Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		if decision.AuditRequired {
			if h.Audit != nil {
				if err := h.Audit(r.Context(), tx, principal, target); err != nil {
					return err
				}
			} else if err := recordConnectionAudit(r.Context(), tx, principal, target, now); err != nil {
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

// recordConnectionAudit records only the exact grant that permitted admission;
// no browser-provided metadata or credential material reaches the audit store.
func recordConnectionAudit(ctx context.Context, tx *sql.Tx, principal auth.Principal, target AuthorizationTarget, now time.Time) error {
	if target.Action != "workspace.connect_admin" || target.Resource.OwnerID == "" {
		return errors.New("invalid audited connection context")
	}
	for _, grant := range target.Grants {
		if grant.ID != "" && grant.SubjectUserID == principal.User.ID && grant.ResourceType == target.Resource.Type && grant.ResourceID == target.Resource.ID && grant.Action == target.Action && grant.RevokedAt == nil {
			_, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: principal.User.ID, Action: target.Action, ResourceType: target.Resource.Type, ResourceID: target.Resource.ID, Outcome: "allowed", ReasonCode: "explicit_grant", Metadata: audit.Metadata{OwnerID: target.Resource.OwnerID, GrantID: grant.ID}})
			return err
		}
	}
	return errors.New("audited connection grant is unavailable")
}
