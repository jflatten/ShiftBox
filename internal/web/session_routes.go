package web

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

const _sessionCookieName = "__Host-shiftbox_session"

// SessionHandler serves the management session read/logout endpoint.
// Lookup must load current membership, session and installation gates together.
// Revoke must commit invalidation and finish live connection closure before returning.
type SessionHandler struct {
	Origin string
	Now    func() time.Time
	Lookup func(context.Context, string) (auth.Principal, error)
	Revoke func(context.Context, string) error
}

// NewSessionHandler wires the active database and revocation registry into the
// normal management session endpoint. Protected bootstrap/recovery flows supply
// their additional verified gates separately; this constructor never infers them.
func NewSessionHandler(db *sql.DB, origin string, registry *proxy.Registry, now func() time.Time) *SessionHandler {
	store := auth.NewStore(db)
	return &SessionHandler{
		Origin: origin,
		Now:    now,
		Lookup: func(ctx context.Context, token string) (auth.Principal, error) {
			var principal auth.Principal
			err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				var err error
				principal, err = store.Principal(ctx, tx, token)
				return err
			})
			return principal, err
		},
		Revoke: func(ctx context.Context, id string) error {
			return store.RevokeAndClose(ctx, id, now(), "session.logout", registry)
		},
	}
}

// ServeHTTP evaluates fresh session authority before returning state or logging out.
func (h *SessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, DELETE")
		WriteProblem(w, RequestID(r.Context()), Problem{Status: 405, Code: "request.method_not_allowed", Title: "Method not allowed"})
		return
	}
	token, err := SessionToken(r)
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	principal, err := h.Lookup(r.Context(), token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
		}
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	now := h.Now()
	action := "session.view"
	if r.Method == http.MethodDelete {
		action = "session.logout"
	}
	if principal.Session.TokenDigest != auth.TokenDigest(token) || !auth.Authorize(principal, action, auth.Resource{}, nil, now).Allowed {
		WriteProblem(w, RequestID(r.Context()), Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"})
		return
	}
	if r.Method == http.MethodDelete {
		MutationGuard(h.Origin, func(*http.Request) (auth.Session, error) { return principal.Session, nil }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := h.Revoke(r.Context(), principal.Session.ID); err != nil {
				WriteProblem(w, RequestID(r.Context()), err)
				return
			}
			ClearSessionCookie(w)
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(w, r)
		return
	}
	var due *time.Time
	if principal.User.CredentialMode == "external" && principal.Session.ExternalAuthorizedAt != nil && principal.ExternalAuthInterval > 0 {
		value := principal.Session.ExternalAuthorizedAt.Add(principal.ExternalAuthInterval).UTC()
		due = &value
	}
	var organizationSecretScopeID string
	organization := auth.Resource{Type: "organization", ID: principal.InstallationID}
	if organization.ID != "" && auth.Authorize(principal, "secret.manage_organization", organization, nil, now).Allowed {
		organizationSecretScopeID = organization.ID
	}
	view := struct {
		User                       auth.User  `json:"user"`
		Mode                       string     `json:"mode"`
		CSRFToken                  string     `json:"csrfToken"`
		Permissions                []string   `json:"permissions"`
		InstallationState          string     `json:"installationState"`
		ExternalAuthorizationDueAt *time.Time `json:"externalAuthorizationDueAt"`
		OrganizationSecretScopeID  string     `json:"organizationSecretScopeId,omitempty"`
	}{principal.User, principal.Session.Mode, auth.CSRFToken(token), sessionPermissions(principal, now), principal.InstallationState, due, organizationSecretScopeID}
	encoded, err := json.Marshal(view)
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded) // A disconnected client cannot receive a replacement response.
}

// sessionPermissions supplies navigation hints, not resource-specific authority.
// Workspace/template actions still require their own current resource decision.
func sessionPermissions(principal auth.Principal, now time.Time) []string {
	permissions := make([]string, 0)
	for _, action := range []string{
		"session.view", "session.logout", "session.change_password", "session.external_reauthorize",
		"setup.view", "setup.mutate", "recovery.review", "recovery.activate",
		"template.create", "notice.list", "notice.read", "notice.dismiss", "notice.publish",
		"user.list", "user.create", "user.approve", "user.disable", "user.change_role", "user.reset_password",
		"audit.list", "infrastructure.view", "infrastructure.manage", "maintenance.manage",
		"backup.create", "restore.validate", "restore.activate", "migration.preflight",
		"secret.manage_user", "git.manage",
	} {
		if auth.Authorize(principal, action, auth.Resource{Type: "user", ID: principal.User.ID}, nil, now).Allowed {
			permissions = append(permissions, action)
		}
	}
	if principal.InstallationID != "" && auth.Authorize(principal, "secret.manage_organization",
		auth.Resource{Type: "organization", ID: principal.InstallationID}, nil, now).Allowed {
		permissions = append(permissions, "secret.manage_organization")
	}
	return permissions
}

// SetSessionCookie issues only the management host's opaque session credential.
// The caller must commit the session before handing its cookie to the browser.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time) error {
	if !validSessionToken(token) || expires.IsZero() {
		return errors.New("invalid session cookie")
	}
	cookie := managementCookie(token)
	cookie.Expires = expires.UTC()
	http.SetCookie(w, &cookie)
	return nil
}

// SessionToken accepts exactly one credential in the controller-issued format.
// Resolving this token still requires current membership and session checks.
func SessionToken(r *http.Request) (string, error) {
	cookies := r.CookiesNamed(_sessionCookieName)
	if len(cookies) != 1 || !validSessionToken(cookies[0].Value) {
		return "", Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
	}
	return cookies[0].Value, nil
}

// ClearSessionCookie expires the same host-only scope used when signing in.
// Durable revocation and live connection closure must happen before logout succeeds.
func ClearSessionCookie(w http.ResponseWriter) {
	cookie := managementCookie("")
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, &cookie)
}

func managementCookie(token string) http.Cookie {
	return http.Cookie{Name: _sessionCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

func validSessionToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == 32
}
