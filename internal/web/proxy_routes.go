package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// ServiceEntryPath exchanges an origin grant before starting a workspace session.
const ServiceEntryPath = "/_shiftbox/entry"
const serviceCookie = "__Host-shiftbox_origin"

const serviceEntryDocument = `<!doctype html><meta charset="utf-8"><title>Open workspace</title><script>location.replace("/")</script><a href="/">Open workspace</a>`

// ServiceProxyHandler serves configured IDE, preview, and repair origins. Forward
// receives requests whose service credential and current authority were checked.
func ServiceProxyHandler(db *sql.DB, hosts *config.ServiceHosts, registry *proxy.Registry, now func() time.Time,
	forward func(http.ResponseWriter, *http.Request, string, int64, string, string),
) http.Handler {
	service := serviceProxy{db: db, hosts: hosts, registry: registry, now: now, forward: forward}
	return WithRequestID(http.HandlerFunc(service.serve))
}

type serviceProxy struct {
	db       *sql.DB
	hosts    *config.ServiceHosts
	registry *proxy.Registry
	now      func() time.Time
	forward  func(http.ResponseWriter, *http.Request, string, int64, string, string)
}

func (s serviceProxy) serve(w http.ResponseWriter, r *http.Request) {
	workspace, service, found := s.hosts.ResolveAuthority(r.Host)
	if r.TLS == nil || !found {
		http.NotFound(w, r)
		return
	}
	if s.db == nil || s.registry == nil || s.now == nil || s.forward == nil {
		WriteProblem(w, RequestID(r.Context()), errors.New("incomplete service proxy"))
		return
	}
	origin := "https://" + r.Host
	if r.URL.Path == ServiceEntryPath {
		s.exchange(w, r, workspace, service, origin)
		return
	}
	if !serviceRequestOrigin(r, origin) {
		writeServiceDenial(w, r, proxy.ErrOriginGrant)
		return
	}
	cookies := r.CookiesNamed(serviceCookie)
	if len(cookies) != 1 {
		writeServiceDenial(w, r, proxy.ErrOriginGrant)
		return
	}
	ctx := r.Context()
	lookup := func(ctx context.Context) (proxy.OriginAuthority, time.Time, error) {
		var authority proxy.OriginAuthority
		var deadline time.Time
		err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
			resource, generation, err := serviceWorkspace(ctx, tx, workspace, service)
			if err != nil {
				return err
			}
			scope := proxy.OriginScope{WorkspaceID: workspace, Generation: generation, Service: service, Origin: origin}
			now := s.now()
			authority, err = proxy.ReadOriginCookie(ctx, tx, scope, cookies[0].Value, now)
			if err != nil {
				return err
			}
			deadline, err = authorizeService(ctx, tx, authority, resource, now)
			return err
		})
		return authority, deadline, err
	}
	authority, deadline, err := lookup(ctx)
	if err != nil {
		writeServiceDenial(w, r, err)
		return
	}
	// Include credential expiry and the external authorization deadline in the
	// active request lifetime. Logout closes the request through the registry.
	active, cancel := context.WithTimeout(ctx, deadline.Sub(s.now()))
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	control := http.NewResponseController(w)
	unregister, err := s.registry.Register(proxy.Scope{UserID: authority.UserID, SessionID: authority.SessionID,
		WorkspaceID: workspace, GrantID: authority.ResourceGrantID, Service: service, IDE: service == "ide" || service == "repair"}, func() error {
		current, currentDeadline, err := lookup(active)
		if err == nil && (current != authority || currentDeadline.Before(deadline)) {
			return proxy.ErrOriginGrant
		}
		return err
	}, func() error {
		cancel()
		// A slow browser write must not prevent revocation from unwinding
		// the forwarder. For an upgraded connection, cancellation closes
		// the upstream and the reverse proxy closes its hijacked transport.
		_ = control.SetWriteDeadline(time.Now())
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			return nil
		case <-timer.C:
			return errors.New("service proxy connection closure pending")
		}
	})
	if err != nil {
		writeServiceDenial(w, r, err)
		return
	}
	defer unregister()
	outgoing := r.Clone(active)
	proxy.StripAuthorityCookies(outgoing)
	s.forward(w, outgoing, workspace, authority.Scope.Generation, service, RequestID(ctx))
}

func (s serviceProxy) exchange(w http.ResponseWriter, r *http.Request, workspace, service, origin string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if r.Method != http.MethodGet || err != nil || len(query) != 1 || len(query["grant"]) != 1 {
		writeServiceDenial(w, r, proxy.ErrOriginGrant)
		return
	}
	var exchange proxy.OriginExchange
	var deadline time.Time
	ctx := r.Context()
	err = database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		resource, generation, err := serviceWorkspace(ctx, tx, workspace, service)
		if err != nil {
			return err
		}
		scope := proxy.OriginScope{WorkspaceID: workspace, Generation: generation, Service: service, Origin: origin}
		now := s.now()
		exchange, err = proxy.ConsumeOriginGrant(ctx, tx, scope, query.Get("grant"), now)
		if err != nil {
			return err
		}
		deadline, err = authorizeService(ctx, tx, exchange.Authority, resource, now)
		return err
	})
	if err != nil {
		writeServiceDenial(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: serviceCookie, Value: exchange.Cookie, Path: "/", Expires: deadline,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	// Commit the entry document on the service origin before navigating. A
	// server redirect would carry the management origin's cross-site context.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'sha256-1tDPdeWNjdqUkS5UOsNLzR7O2uYYppPG16kqscmrgtQ='; frame-ancestors 'none'; base-uri 'none'")
	_, _ = io.WriteString(w, serviceEntryDocument)
}

func authorizeService(ctx context.Context, tx *sql.Tx, authority proxy.OriginAuthority, resource auth.Resource, now time.Time) (time.Time, error) {
	store := auth.NewStore(nil)
	principal, err := store.PrincipalForSession(ctx, tx, authority.SessionID)
	if err != nil {
		return time.Time{}, err
	}
	if principal.User.ID != authority.UserID {
		return time.Time{}, proxy.ErrOriginGrant
	}
	var grants []auth.ResourceGrant
	if authority.ResourceGrantID != "" {
		current, err := store.ActiveGrants(ctx, tx, authority.UserID, "workspace", authority.Scope.WorkspaceID)
		if err != nil {
			return time.Time{}, err
		}
		for _, grant := range current {
			if grant.ID == authority.ResourceGrantID {
				grants = append(grants, grant)
			}
		}
	}
	if !auth.Authorize(principal, authority.Permission, resource, grants, now).Allowed {
		return time.Time{}, proxy.ErrOriginGrant
	}
	deadline := principal.Session.ExpiresAt
	if principal.User.CredentialMode == "external" {
		if external := principal.Session.ExternalAuthorizedAt.Add(principal.ExternalAuthInterval); external.Before(deadline) {
			deadline = external
		}
	}
	if authority.CookieExpiresAt.Before(deadline) {
		deadline = authority.CookieExpiresAt
	}
	return deadline, nil
}

func serviceRequestOrigin(r *http.Request, origin string) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 0 {
		return len(origins) == 1 && origins[0] == origin
	}
	return r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
}

func writeServiceDenial(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, proxy.ErrOriginGrant) {
		err = Problem{Status: 403, Code: "workspace.entry_denied", Title: "Workspace service access denied"}
	}
	WriteProblem(w, RequestID(r.Context()), err)
}

// WorkspaceEntryGrantHandler issues a short-lived exchange URL for one
// service after the normal management session, CSRF and policy checks.
func WorkspaceEntryGrantHandler(db *sql.DB, origin string, now func() time.Time, hosts *config.ServiceHosts) *AuthorizedHandler {
	store := auth.NewStore(db)
	return &AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/workspaces/{workspaceId}/entry-grants",
			OperationID: "createWorkspaceEntryGrant", Resolver: "workspace_entry"},
		Resolve: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal) (AuthorizationTarget, error) {
			var input struct {
				Service string `json:"service"`
			}
			if err := DecodeJSON(nil, r, &input, 1024); err != nil {
				return AuthorizationTarget{}, err
			}
			workspace := r.PathValue("workspaceId")
			host, err := hosts.ServiceAuthority(workspace, input.Service)
			if err != nil {
				return AuthorizationTarget{}, Problem{Status: 400, Code: "request.invalid_service", Title: "Invalid workspace service"}
			}
			resource, generation, err := serviceWorkspace(ctx, tx, workspace, input.Service)
			if errors.Is(err, sql.ErrNoRows) {
				return AuthorizationTarget{}, Problem{Status: 404, Code: "workspace.not_found", Title: "Workspace not found"}
			}
			if err != nil {
				return AuthorizationTarget{}, err
			}
			target, err := serviceEntryTarget(ctx, tx, store, principal, resource)
			target.entry = &proxy.OriginScope{WorkspaceID: workspace, Generation: generation, Service: input.Service, Origin: "https://" + host}
			return target, err
		},
		Apply: func(ctx context.Context, tx *sql.Tx, _ *http.Request, principal auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			binding := proxy.OriginAuthority{Scope: *target.entry, UserID: principal.User.ID,
				SessionID: principal.Session.ID, Permission: target.Action}
			if target.Action == "workspace.connect_admin" {
				for _, grant := range target.Grants {
					if grant.Action == target.Action {
						binding.ResourceGrantID = grant.ID
						break
					}
				}
			}
			token, expiry, err := proxy.IssueOriginGrant(ctx, tx, binding, now())
			if err != nil {
				return nil, err
			}
			result := struct {
				ExchangeURL string    `json:"exchangeUrl"`
				ExpiresAt   time.Time `json:"expiresAt"`
			}{binding.Scope.Origin + ServiceEntryPath + "?" + url.Values{"grant": {token}}.Encode(), expiry}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(result)
			}), nil
		},
	}
}

func serviceEntryTarget(ctx context.Context, tx *sql.Tx, store *auth.Store, principal auth.Principal, resource auth.Resource) (AuthorizationTarget, error) {
	target := AuthorizationTarget{Action: "workspace.connect_owner", Resource: resource}
	if resource.OwnerID == principal.User.ID {
		return target, nil
	}
	target.Action = "workspace.connect_admin"
	grants, err := store.ActiveGrants(ctx, tx, principal.User.ID, "workspace", resource.ID)
	target.Grants = grants
	return target, err
}

func serviceWorkspace(ctx context.Context, tx *sql.Tx, id, service string) (auth.Resource, int64, error) {
	resource := auth.Resource{Type: "workspace", ID: id}
	var generation int64
	var previews string
	err := tx.QueryRowContext(ctx, `SELECT w.owner_user_id,w.generation,w.preview_ports_json,
		(w.desired_state='running' AND w.observed_state='running' AND i.state='active'
		AND owner.membership_state='active' AND ($2 NOT IN ('ide','repair') OR EXISTS (
		 SELECT 1 FROM operation_steps s
		 WHERE s.step_key=CASE WHEN $2='repair' THEN 'verify_repair' ELSE 'verify_ide' END
		 AND s.status='succeeded' AND s.operation_id=(
		  SELECT o.id FROM operations o
		  WHERE o.workspace_id=w.id AND o.generation=w.generation
		    AND o.originating_action IN ('workspace.launch','workspace.start','workspace.rebuild','workspace.repair')
		  ORDER BY o.created_at DESC,o.id DESC LIMIT 1
		 )
		)))
		FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN users owner ON owner.id=w.owner_user_id
		WHERE w.id=$1`, id, service).Scan(&resource.OwnerID, &generation, &previews, &resource.Eligible)
	if err == nil && service != "ide" && service != "repair" && resource.Eligible {
		port, ok := workspace.PreviewPort(service)
		ports, parseErr := workspace.ParsePreviewPorts(previews)
		resource.Eligible = ok && parseErr == nil && slices.Contains(ports, port)
	}
	return resource, generation, err
}
