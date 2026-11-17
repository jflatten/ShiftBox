package app

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// ManagementServices groups the handlers built from one active installation.
type ManagementServices struct {
	Session        *web.SessionHandler
	Handlers       map[string]web.AuthorizedHandler
	GitHubCallback http.Handler
}

// NewManagementServices binds workspace, template, operation, capacity, and Git
// services to shared controller state. The active router checks its catalog
// separately from the protected setup listener.
func NewManagementServices(state *ControllerState, journal *operation.Journal,
	registry *proxy.Registry, launchPolicy web.WorkspaceLaunchPolicy, github *identity.GitHub, passwords identity.PasswordAdmin, now func() time.Time,
) (ManagementServices, error) {
	if state == nil || state.DB == nil || journal == nil || registry == nil || now == nil {
		return ManagementServices{}, errors.New("management services are incompletely configured")
	}
	db, settings := state.DB, state.Installation.Settings
	if state.retentionMaximum != nil {
		launchPolicy.RetentionMaximum = state.retentionMaximum
	}
	origin := settings.ManagementOrigin
	hosts, err := config.NewServiceHosts(origin, settings.IDEHostPattern, settings.PreviewHostPattern)
	if err != nil {
		return ManagementServices{}, err
	}
	listWorkspaces, getWorkspace := web.WorkspaceReadHandlers(db, origin, now, state.workspaceRetentionStatus)
	getOperation, streamEvents := web.OperationHandlers(db, origin, now, journal, registry)
	listTemplates, createTemplate, reviseTemplate := web.TemplateHandlers(db, origin, now, uuid.NewString)
	grantTemplate, revokeTemplate := web.TemplateGrantHandlers(db, origin, now)
	budget, resourceProfile := web.CapacityHandlers(db, origin, now, uuid.NewString, launchPolicy)
	publishNotice, listNotices, noticeReceipt := web.NoticeHandlers(db, origin, now, uuid.NewString)
	listMaintenance, createMaintenance := web.MaintenanceHandlers(db, origin, now, journal)
	handlers := web.GitAuthorizationHandlers(db, origin, now, github)
	for id, handler := range web.SecretHandlers(db, origin, state.Secrets, now) {
		handlers[id] = handler
	}
	for _, handler := range web.ResourceProfileHandlers(db, origin, now, launchPolicy) {
		handlers[handler.Route.OperationID] = handler
	}
	var passwordChange sync.Mutex
	listConnectionGrants, revokeConnectionGrant := web.WorkspaceConnectionGrantHandlers(db, origin, now, registry)
	for _, handler := range []web.AuthorizedHandler{
		listConnectionGrants, revokeConnectionGrant,
		listWorkspaces, getWorkspace, getOperation, streamEvents, listTemplates, createTemplate, reviseTemplate, budget, resourceProfile,
		web.TemplateCatalogHandler(db, origin, now, launchPolicy),
		web.InfrastructureStatusHandler(db, origin, now, launchPolicy.Capabilities),
		web.KeepAlivePolicyHandler(db, origin, now),
		web.NetworkPolicyHandler(db, origin, now, uuid.NewString, launchPolicy.Capabilities),
		state.browserCertificateHandler(journal, now),
		state.browserCertificateReadHandler(now),
		activePasswordHandler(db, origin, registry, passwords, now, &passwordChange),
		resetPasswordHandler(db, origin, registry, passwords, now, &passwordChange),
		state.createLocalUserHandler(passwords, now),
		web.ListUsersHandler(db, origin, now),
		web.ListAuditEventsHandler(db, origin, now),
		publishNotice, listNotices, noticeReceipt, listMaintenance, createMaintenance,
		web.UpdateMembershipHandler(db, origin, now, registry, journal),
		web.TemplateUpdateHandler(db, origin, now), grantTemplate, revokeTemplate,
		web.WorkspaceLaunchHandler(db, origin, now, journal, launchPolicy),
		web.WorkspacePowerHandler(db, origin, now, journal, registry),
		web.WorkspacePolicyHandler(db, origin, now, launchPolicy, registry),
		*web.WorkspaceEntryGrantHandler(db, origin, now, hosts),
	} {
		handlers[handler.Route.OperationID] = handler
	}
	return ManagementServices{
		Session: web.NewSessionHandler(db, origin, registry, now), Handlers: handlers,
		GitHubCallback: web.GitAuthorizationCallback(origin, github),
	}, nil
}

// NewManagementSite mounts the API, login and identity handlers alongside the
// embedded browser assets. The caller serves it only on the management origin.
func NewManagementSite(api, login, dex, assets http.Handler) (http.Handler, error) {
	if api == nil || login == nil || dex == nil || assets == nil {
		return nil, errors.New("management site is incompletely configured")
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", api)
	mux.Handle("/auth/", login)
	mux.Handle("/dex/", dex)
	mux.Handle("/setup", http.NotFoundHandler())
	mux.Handle("/setup/", http.NotFoundHandler())
	mux.Handle("/", assets)
	return mux, nil
}
