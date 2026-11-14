package app

import (
	"context"
	"database/sql"
	"encoding/json"

	"errors"
	"log/slog"

	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

type connectorAdmin interface {
	Create(context.Context, string, string, identity.ExternalConnectorConfig) error
	Delete(context.Context, string) error
}

type externalConfiguration struct {
	db       *sql.DB
	origin   string
	registry *proxy.Registry
	dex      connectorAdmin
	discover func(context.Context, identity.ExternalConnectorConfig) error
	now      func() time.Time
	mu       sync.Mutex
}

type externalConnectorInput struct {
	Enabled                      *bool  `json:"enabled"`
	Issuer                       string `json:"issuer"`
	ConnectorID                  string `json:"connectorId"`
	ClientID                     string `json:"clientId"`
	ClientSecret                 string `json:"clientSecret"`
	AuthorizationIntervalSeconds int64  `json:"authorizationIntervalSeconds"`
}

// CurrentExternalConnector reads the one connector authorized to start a flow.
func CurrentExternalConnector(ctx context.Context, db *sql.DB) (identity.ConnectorIdentity, error) {
	var connector identity.ConnectorIdentity
	err := db.QueryRowContext(ctx, `SELECT c.issuer,c.connector_id,c.dex_id FROM external_connector c
		JOIN installations i ON c.installation_id=i.id WHERE i.singleton=1 AND i.state IN ('active','blocked')`).
		Scan(&connector.Issuer, &connector.ID, &connector.DexID)
	return connector, err
}

// ExternalConfigurationHandler activates the administrator's selected upstream.
func ExternalConfigurationHandler(db *sql.DB, origin string, registry *proxy.Registry, dex identity.ExternalConnectorAdmin, now func() time.Time) web.AuthorizedHandler {
	service := &externalConfiguration{db: db, origin: origin, registry: registry, dex: dex, now: now,
		discover: func(ctx context.Context, cfg identity.ExternalConnectorConfig) error { return cfg.Discover(ctx) }}
	return service.handler()
}

func (s *externalConfiguration) handler() web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: s.db, Origin: s.origin, Now: s.now,
		Route: web.ManagementRoute{Method: http.MethodPut, Path: "/admin/identity/external", OperationID: "configureExternalIdentity", Action: "infrastructure.manage"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "infrastructure.manage"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			version, err := web.RequireVersion(r)
			if err != nil {
				return nil, err
			}
			var current int64
			if err := tx.QueryRowContext(ctx, "SELECT config_generation FROM installations WHERE singleton=1").Scan(&current); err != nil {
				return nil, err
			}
			if version != current {
				return nil, staleExternalConfig()
			}
			var input externalConnectorInput
			if err := web.DecodeJSON(nil, r, &input, 8192); err != nil {
				return nil, err
			}
			if input.Enabled == nil || input.AuthorizationIntervalSeconds <= 0 ||
				input.AuthorizationIntervalSeconds > int64((1<<63-1)/time.Second) {
				return nil, invalidExternalConfig()
			}
			cfg := identity.ExternalConnectorConfig{ID: input.ConnectorID, Issuer: input.Issuer, ClientID: input.ClientID, ClientSecret: input.ClientSecret}
			if *input.Enabled && cfg.Validate() != nil {
				return nil, invalidExternalConfig()
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token, err := web.SessionToken(r)
				if err == nil {
					err = s.configure(r.Context(), version, token, *input.Enabled, input.AuthorizationIntervalSeconds, cfg)
				}
				if err != nil {
					web.WriteProblem(w, web.RequestID(r.Context()), err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}), nil
		},
	}
}

func (s *externalConfiguration) configure(ctx context.Context, generation int64, token string, enabled bool, interval int64, cfg identity.ExternalConnectorConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var staged string
	if enabled {
		if err := s.discover(ctx, cfg); err != nil {
			return invalidExternalConfig()
		}
		staged = "shiftbox-external-" + uuid.NewString()
		if err := s.dex.Create(ctx, staged, s.origin, cfg); err != nil {
			s.retireConnector(ctx, staged)
			return externalConfigUnavailable()
		}
	}
	var previous string
	affected := make(map[string]bool)
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := lockExternalSessionWrite(ctx, tx); err != nil {
			return err
		}
		now := s.now()
		principal, err := auth.NewStore(s.db).Principal(ctx, tx, token)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !auth.Authorize(principal, "infrastructure.manage", auth.Resource{}, nil, now).Allowed) {
			return web.Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		if err != nil {
			return err
		}
		if !enabled {
			var localAdmin bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE role='administrator'
				AND credential_mode='local' AND membership_state='active' AND must_change_password=0)`).Scan(&localAdmin); err != nil {
				return err
			}
			if !localAdmin {
				return web.Problem{Status: 409, Code: "user.last_administrator", Title: "Keep an active local administrator before disabling organizational sign-in"}
			}
		}
		activation := config.Activation{ExpectedGeneration: generation, ActorID: principal.User.ID, ActivatedAt: now}
		settings, err := currentIdentitySettings(ctx, tx, &activation)
		if err != nil {
			return err
		}
		settings.ExternalAuthIntervalSeconds = interval
		_, err = config.RecordInstallationSettings(ctx, tx, activation, settings)
		if errors.Is(err, config.ErrActivationConflict) {
			return staleExternalConfig()
		}
		if err != nil {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT dex_id FROM external_connector WHERE installation_id=$1", activation.InstallationID).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM external_connector WHERE installation_id=$1", activation.InstallationID); err != nil {
			return err
		}
		if enabled {
			if _, err := tx.ExecContext(ctx, `INSERT INTO external_connector(installation_id,issuer,connector_id,dex_id)
				VALUES($1,$2,$3,$4)`, activation.InstallationID, cfg.Issuer, cfg.ID, staged); err != nil {
				return err
			}
		}
		if err := revokeExternalConfigurationSessions(ctx, tx, now, affected); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: principal.User.ID,
			Action: "infrastructure.manage", ResourceType: "installation", ResourceID: activation.InstallationID,
			Outcome: "allowed", ReasonCode: "external_connector_configured"})
		return err
	})
	if err != nil {
		// A commit error can have an uncertain result. Verify the active ID
		// before removing the staged connector.
		confirm, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		active, readErr := CurrentExternalConnector(confirm, s.db)
		if errors.Is(readErr, sql.ErrNoRows) || (readErr == nil && active.DexID != staged) {
			s.retireConnector(confirm, staged)
		}
		return err
	}
	s.retireConnector(ctx, previous)
	if err := s.registry.CloseMatching(func(scope proxy.Scope) bool { return affected[scope.SessionID] }); err != nil {
		return externalConfigUnavailable()
	}
	return nil
}

func currentIdentitySettings(ctx context.Context, tx *sql.Tx, activation *config.Activation) (config.InstallationSettings, error) {
	var generation int64
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT i.id,i.config_generation,a.settings_json FROM installations i
		JOIN activated_configurations a ON a.installation_id=i.id AND a.generation=i.config_generation AND a.retired_at IS NULL
		WHERE i.singleton=1 AND i.state='active'`).Scan(&activation.InstallationID, &generation, &raw)
	if err != nil {
		return config.InstallationSettings{}, err
	}
	if generation != activation.ExpectedGeneration {
		return config.InstallationSettings{}, staleExternalConfig()
	}
	var settings config.InstallationSettings
	err = json.Unmarshal([]byte(raw), &settings)
	return settings, err
}

func revokeExternalConfigurationSessions(ctx context.Context, tx *sql.Tx, now time.Time, affected map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id FROM sessions s JOIN users u ON s.user_id=u.id
		WHERE u.credential_mode='external'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		affected[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason='external_connector_changed'
		WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM users WHERE credential_mode='external')`, stamp); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE origin_grants SET revoked_at=$1
		WHERE revoked_at IS NULL AND user_id IN (SELECT id FROM users WHERE credential_mode='external')`, stamp)
	return err
}

func (s *externalConfiguration) retireConnector(ctx context.Context, id string) {
	if id == "" {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := s.dex.Delete(cleanup, id); err != nil {
		slog.WarnContext(cleanup, "Unable to remove an inactive Dex connector")
	}
}

func staleExternalConfig() web.Problem {
	return web.Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh configuration before saving changes"}
}

func invalidExternalConfig() web.Problem {
	return web.Problem{Status: 400, Code: "auth.invalid_external_configuration", Title: "Provide a valid HTTPS issuer, connector, client credentials, and authorization interval"}
}

func externalConfigUnavailable() web.Problem {
	return web.Problem{Status: 503, Code: "auth.external_unavailable", Title: "External identity configuration could not be completed"}
}
