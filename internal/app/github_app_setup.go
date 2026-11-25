package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	githubAppReturn          = "/api/v1/admin/settings/github-app/return"
	githubAppExchangeTimeout = 20 * time.Second
)

type githubAppSetup struct {
	db             *sql.DB
	cipher         *cryptostore.Store
	installation   string
	origin         string
	name           string
	now            func() time.Time
	client         *http.Client
	publish        func(*identity.GitHub)
	fileManaged    bool
	installationMu sync.Mutex
}

type githubAppRecord struct {
	Organization        string
	State               string
	Session             string
	Expires             time.Time
	Claimed             time.Time
	App                 *githubAppRegistration
	InstallationID      int64
	RepositorySelection string
	Revoking            bool
}

type githubAppStatus struct {
	State               string `json:"state"`
	AppName             string `json:"appName,omitempty"`
	Owner               string `json:"owner,omitempty"`
	InstallURL          string `json:"installUrl,omitempty"`
	ManageURL           string `json:"manageUrl,omitempty"`
	RepositorySelection string `json:"repositorySelection,omitempty"`
	InstallationID      string `json:"installationId,omitempty"`
}

// ConfigureGitHubAppSetup loads durable credentials and wires the active client
// that user authorization and guest Git endpoints share.
func (s *ControllerState) ConfigureGitHubAppSetup(ctx context.Context, file *identity.GitHub, now func() time.Time) (*identity.GitHub, error) {
	installation := s.Installation.InstallationID
	origin := s.Installation.Settings.ManagementOrigin
	if s.DB == nil || s.cipher == nil || !web.ValidUUID(installation) || now == nil {
		return nil, errors.New("GitHub App setup requires the active installation database")
	}
	runtime, publish := identity.NewGitHubRuntime(file)
	setup := &githubAppSetup{db: s.DB, cipher: s.cipher, installation: installation, origin: origin,
		name: "ShiftBox " + installation[:8], now: now, publish: publish, fileManaged: file != nil,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
	if !setup.fileManaged {
		record, _, err := setup.read(ctx, s.DB)
		if err != nil {
			return nil, err
		}
		if record.App != nil && record.InstallationID > 0 && !record.Revoking {
			client, err := setup.userClient(record)
			if err != nil {
				return nil, err
			}
			publish(client)
		}
	}
	s.githubAppSetup = setup
	return runtime, nil
}

func (s *githubAppSetup) userClient(record githubAppRecord) (*identity.GitHub, error) {
	return identity.NewGitHub(s.db, s.cipher, record.App.ClientID, record.App.ClientSecret, record.InstallationID, s.now)
}

func (s *githubAppSetup) binding() cryptostore.Context {
	const kind = "github_app_setup"
	return cryptostore.Context{KeyVersion: 1, RecordType: kind, Scope: "installation",
		OwnerID: s.installation, RecordID: kind + ":" + s.installation, Name: "github.com"}
}

func (s *githubAppSetup) read(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (githubAppRecord, string, error) {
	var record githubAppRecord
	var encoded string
	binding := s.binding()
	err := db.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", binding.RecordID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return record, "", nil
	}
	if err != nil {
		return record, "", err
	}
	var value cryptostore.Value
	if json.Unmarshal([]byte(encoded), &value) != nil {
		return record, "", errors.New("invalid protected GitHub App setup record")
	}
	raw, err := s.cipher.Open(binding, value)
	defer clear(raw)
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return record, "", errors.New("could not open protected GitHub App setup record")
	}
	return record, encoded, nil
}

// write compares the previously read ciphertext so a stale callback cannot
// supersede another administrator's setup or a completed installation.
func (s *githubAppSetup) write(ctx context.Context, tx *sql.Tx, previous string, record githubAppRecord) (string, error) {
	raw, err := json.Marshal(record)
	defer clear(raw)
	if err != nil {
		return "", err
	}
	binding := s.binding()
	value, err := s.cipher.Seal(binding, raw)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var result sql.Result
	if previous == "" {
		result, err = tx.ExecContext(ctx, `INSERT INTO protected_values(id,value_json)
			VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, binding.RecordID, string(encoded))
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE protected_values
			SET value_json=$2 WHERE id=$1 AND value_json=$3`, binding.RecordID, string(encoded), previous)
	}
	if err != nil {
		return "", err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return "", githubAppConflict()
	}
	return string(encoded), nil
}

func (s *githubAppSetup) status(record githubAppRecord) githubAppStatus {
	if s.fileManaged {
		return githubAppStatus{State: "file_managed"}
	}
	if record.App != nil {
		view := githubAppStatus{State: "installation_pending", AppName: record.App.Name, Owner: record.App.Owner.Login}
		if record.InstallationID == 0 {
			view.InstallURL = record.App.installURL()
		} else {
			view.InstallationID = strconv.FormatInt(record.InstallationID, 10)
			if record.Revoking {
				view.State = "revoking"
			} else {
				view.State = "ready"
				view.ManageURL = record.App.manageURL(record.InstallationID)
				view.RepositorySelection = record.RepositorySelection
			}
		}
		return view
	}
	if !record.Claimed.IsZero() {
		if s.now().Before(record.Claimed.Add(githubAppExchangeTimeout)) {
			return githubAppStatus{State: "exchanging"}
		}
		return githubAppStatus{State: "recovery_required"}
	}
	if record.State != "" && s.now().Before(record.Expires) {
		return githubAppStatus{State: "registering", Owner: record.Organization}
	}
	return githubAppStatus{State: "not_configured"}
}

func (s *ControllerState) githubAppHandlers(now func() time.Time) map[string]web.AuthorizedHandler {
	handlers := make(map[string]web.AuthorizedHandler, 5)
	setup := s.githubAppSetup
	for _, route := range web.ManagementRoutes() {
		switch route.OperationID {
		case "getGitHubAppSetup", "beginGitHubAppSetup", "completeGitHubAppRegistration", "verifyGitHubAppInstallation", "revokeGitHubAppInstallation":
		default:
			continue
		}
		handlers[route.OperationID] = web.AuthorizedHandler{
			DB: s.DB, Origin: s.Installation.Settings.ManagementOrigin, Now: now, Route: route,
			Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
				return web.AuthorizationTarget{Action: "infrastructure.manage", Resource: auth.Resource{Type: "installation", ID: s.Installation.InstallationID}}, nil
			},
			Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
				if setup == nil {
					return nil, web.Problem{Status: 503, Code: "github_app.unavailable", Title: "GitHub App setup is unavailable"}
				}
				if setup.fileManaged {
					if route.OperationID == "getGitHubAppSetup" {
						return githubAppJSON(setup.status(githubAppRecord{})), nil
					}
					return nil, githubAppConflict()
				}
				record, previous, err := setup.read(ctx, tx)
				if err != nil {
					return nil, err
				}
				switch route.OperationID {
				case "getGitHubAppSetup":
					return githubAppJSON(setup.status(record)), nil
				case "beginGitHubAppSetup":
					return setup.begin(ctx, tx, r, principal, record, previous)
				case "completeGitHubAppRegistration":
					return setup.callback(ctx, tx, r, principal, record, previous)
				case "verifyGitHubAppInstallation":
					return setup.verify(principal, record, previous)
				case "revokeGitHubAppInstallation":
					return setup.revoke(ctx, tx, r, principal, record, previous)
				}
				return nil, githubAppConflict()
			},
		}
	}
	return handlers
}

func (s *githubAppSetup) begin(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal,
	record githubAppRecord, previous string) (http.Handler, error) {
	var input struct {
		Organization string `json:"organization"`
		Restart      bool   `json:"restart"`
	}
	if err := web.DecodeJSON(nil, r, &input, 1024); err != nil {
		return nil, err
	}
	input.Organization = strings.ToLower(strings.TrimSpace(input.Organization))
	if input.Organization != "" && !githubOwnerName.MatchString(input.Organization) {
		return nil, web.Problem{Status: 400, Code: "github_app.invalid_organization", Title: "Enter a GitHub organization name"}
	}
	phase := s.status(record).State
	switch phase {
	case "not_configured":
	case "registering":
		if record.Session != principal.Session.ID || record.Organization != input.Organization {
			return nil, githubAppConflict()
		}
		return s.form(record)
	case "recovery_required":
		if !input.Restart {
			return nil, githubAppConflict()
		}
	default:
		return nil, githubAppConflict()
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	record = githubAppRecord{Organization: input.Organization, Session: principal.Session.ID,
		State: base64.RawURLEncoding.EncodeToString(random), Expires: s.now().Add(30 * time.Minute)}
	if _, err := s.write(ctx, tx, previous, record); err != nil {
		return nil, err
	}
	if err := s.audit(ctx, tx, principal.User.ID, "github_app.begin"); err != nil {
		return nil, err
	}
	return s.form(record)
}

func (s *githubAppSetup) form(record githubAppRecord) (http.Handler, error) {
	manifest, err := json.Marshal(map[string]any{
		"name": s.name, "url": s.origin, "description": "Repository access for ShiftBox workspaces",
		"redirect_url": s.origin + githubAppReturn, "setup_url": s.origin + web.GitHubAppIntegrationsPath + "?github-install=1",
		"callback_urls": []string{s.origin + "/api/v1/git/authorization/callback"},
		"public":        false, "request_oauth_on_install": false, "setup_on_update": false,
		"default_permissions": githubPermissions, "default_events": []string{},
		"hook_attributes": map[string]any{"url": s.origin, "active": false},
	})
	if err != nil {
		return nil, err
	}
	path := "/settings/apps/new"
	if record.Organization != "" {
		path = "/organizations/" + record.Organization + path
	}
	return githubAppJSON(struct {
		Action   string `json:"action"`
		Manifest string `json:"manifest"`
	}{"https://github.com" + path + "?state=" + url.QueryEscape(record.State), string(manifest)}), nil
}

func (s *githubAppSetup) callback(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal,
	record githubAppRecord, previous string) (http.Handler, error) {
	var input struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := web.DecodeJSON(nil, r, &input, 1024); err != nil {
		return nil, err
	}
	if !githubAppCode.MatchString(input.Code) ||
		s.status(record).State != "registering" || record.Session != principal.Session.ID ||
		subtle.ConstantTimeCompare([]byte(record.State), []byte(input.State)) != 1 {
		return nil, web.Problem{Status: 400, Code: "github_app.invalid_state", Title: "Return to GitHub App settings to start setup"}
	}
	record.Claimed = s.now()
	claimed, err := s.write(ctx, tx, previous, record)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		// The conversion code is single-use. Finish its bounded exchange and
		// persistence even if the browser closes after the request is admitted.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), githubAppExchangeTimeout)
		defer cancel()
		app, err := s.convert(ctx, input.Code, record.Organization)
		if err != nil {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 502, Code: "github_app.registration_failed",
				Title:  "GitHub App registration could not be completed",
				Detail: "Check your GitHub App settings before starting over."})
			return
		}
		record = githubAppRecord{App: app}
		if err := s.finish(ctx, principal, claimed, record, "github_app.register"); err != nil {
			web.WriteProblem(w, web.RequestID(r.Context()), err)
			return
		}
		githubAppJSON(s.status(record)).ServeHTTP(w, r)
	}), nil
}

func (s *githubAppSetup) verify(principal auth.Principal, record githubAppRecord, previous string) (http.Handler, error) {
	if record.App == nil {
		return nil, githubAppConflict()
	}
	if record.InstallationID > 0 {
		return githubAppJSON(s.status(record)), nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		install, err := s.findInstallation(r.Context(), *record.App)
		if err != nil {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 502, Code: "github_app.verification_failed",
				Title: "GitHub installation could not be verified", Retryable: true})
			return
		}
		if install != nil {
			record.InstallationID, record.RepositorySelection = install.ID, install.RepositorySelection
			loaded, err := s.userClient(record)
			if err == nil {
				s.installationMu.Lock()
				err = s.finish(r.Context(), principal, previous, record, "github_app.install")
				if err == nil {
					s.publish(loaded)
				}
				s.installationMu.Unlock()
			}
			if err != nil {
				web.WriteProblem(w, web.RequestID(r.Context()), err)
				return
			}
		}
		githubAppJSON(s.status(record)).ServeHTTP(w, r)
	}), nil
}

func (s *githubAppSetup) revoke(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal,
	record githubAppRecord, previous string) (http.Handler, error) {
	var input struct {
		InstallationID string `json:"installationId"`
	}
	if err := web.DecodeJSON(nil, r, &input, 1024); err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(input.InstallationID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != input.InstallationID {
		return nil, web.Problem{Status: 400, Code: "github_app.invalid_installation", Title: "A valid installation ID is required"}
	}
	if record.App == nil || (record.InstallationID != 0 && record.InstallationID != id) {
		return nil, githubAppConflict()
	}
	if record.InstallationID == 0 {
		return githubAppJSON(s.status(record)), nil
	}
	if !record.Revoking {
		record.Revoking = true
		if _, err := s.write(ctx, tx, previous, record); err != nil {
			return nil, err
		}
		if err := identity.InvalidateGitHubAppAuthorizationsInTx(ctx, tx, record.App.ClientID, s.now()); err != nil {
			return nil, err
		}
		if err := s.audit(ctx, tx, principal.User.ID, "github_app.revoke_requested"); err != nil {
			return nil, err
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The durable request was authorized before contacting GitHub. Finish
		// recording its outcome even if the browser closes in the meantime.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), githubAppExchangeTimeout)
		defer cancel()
		result, err := s.completeRevocation(ctx, principal.User.ID, id)
		if err != nil {
			web.WriteProblem(w, web.RequestID(r.Context()), err)
			return
		}
		githubAppJSON(result).ServeHTTP(w, r)
	}), nil
}

func (s *githubAppSetup) completeRevocation(ctx context.Context, actor string, id int64) (githubAppStatus, error) {
	// Keep runtime publication ordered with installation verification and
	// serialize retries so a stale request cannot disable a new installation.
	s.installationMu.Lock()
	defer s.installationMu.Unlock()
	record, previous, err := s.read(ctx, s.db)
	if err != nil {
		return githubAppStatus{}, err
	}
	if record.App != nil && record.InstallationID == 0 {
		return s.status(record), nil
	}
	if record.App == nil || !record.Revoking || record.InstallationID != id {
		return githubAppStatus{}, githubAppConflict()
	}
	s.publish(nil)
	removed, err := s.removeInstallation(ctx, *record.App, id)
	if err != nil {
		return githubAppStatus{}, web.Problem{Status: 502, Code: "github_app.revocation_failed",
			Title: "GitHub installation removal could not be confirmed", Retryable: true}
	}
	if !removed {
		return s.status(record), nil
	}
	record.Revoking = false
	record.InstallationID = 0
	record.RepositorySelection = ""
	if err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := s.write(ctx, tx, previous, record); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "github_app.revoke")
	}); err != nil {
		return githubAppStatus{}, err
	}
	return s.status(record), nil
}

func (s *githubAppSetup) finish(ctx context.Context, principal auth.Principal, previous string, record githubAppRecord, action string) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := auth.NewStore(s.db).PrincipalForSession(ctx, tx, principal.Session.ID)
		if err != nil || !auth.Authorize(current, "infrastructure.manage", auth.Resource{}, nil, s.now()).Allowed {
			return web.Problem{Status: 403, Code: "auth.forbidden", Title: "Administrator access is required"}
		}
		if _, err := s.write(ctx, tx, previous, record); err != nil {
			return err
		}
		if record.InstallationID > 0 {
			if err := identity.InvalidateGitHubAppAuthorizationsInTx(ctx, tx, record.App.ClientID, s.now()); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, current.User.ID, action)
	})
}

func (s *githubAppSetup) audit(ctx context.Context, tx *sql.Tx, actor, action string) error {
	_, err := audit.Append(ctx, tx, audit.Event{OccurredAt: s.now(), ActorType: "user", ActorID: actor,
		Action: action, ResourceType: "installation", ResourceID: s.installation, Outcome: "allowed", ReasonCode: "administrator_setup"})
	return err
}

func githubAppConflict() web.Problem {
	return web.Problem{Status: 409, Code: "github_app.setup_conflict", Title: "Refresh GitHub App settings and resume setup"}
}

func githubAppJSON(value any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	})
}
