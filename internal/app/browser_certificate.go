package app

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const replaceCertificateStep = "replace_browser_certificate"

type servedBrowserCertificate struct {
	generation int64
	pair       tls.Certificate
}

func (s *ControllerState) browserCertificateAt(now time.Time) (*tls.Certificate, error) {
	pair := &s.BrowserTLS
	if current := s.browserReplacement.Load(); current != nil {
		pair = &current.pair
	}
	if len(pair.Certificate) == 0 || pair.PrivateKey == nil || pair.Leaf == nil ||
		now.Before(pair.Leaf.NotBefore) || !now.Before(pair.Leaf.NotAfter) {
		return nil, config.ErrInvalidBrowserCertificate
	}
	return pair, nil
}

func (s *ControllerState) browserCertificateHandler(journal *operation.Journal, now func() time.Time) web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: s.DB, Origin: s.Installation.Settings.ManagementOrigin, Now: now,
		Route: web.ManagementRoute{Method: http.MethodPut, Path: "/admin/certificate", OperationID: "replaceBrowserCertificate", Action: "infrastructure.manage"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "infrastructure.manage"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			key, err := web.IdempotencyKey(r)
			if err != nil {
				return nil, err
			}
			var input struct {
				Chain string `json:"certificateChainPem"`
				Key   string `json:"privateKeyPem"`
			}
			if err := web.DecodeJSON(nil, r, &input, 256<<10); err != nil {
				return nil, err
			}
			var priorID string
			err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", actor.User.ID, key).Scan(&priorID)
			switch {
			case err == nil:
				op, err := journal.GetInTx(ctx, tx, priorID)
				if err != nil {
					return nil, err
				}
				if op.OriginatingAction != "infrastructure.manage" || op.Status != "succeeded" ||
					len(op.Steps) != 1 || op.Steps[0].Key != replaceCertificateStep {
					return nil, web.Problem{Status: 409, Code: "operation.conflict", Title: "Use a new request key"}
				}
				return certificateResponse(op), nil
			case !errors.Is(err, sql.ErrNoRows):
				return nil, err
			}
			if s.cipher == nil || s.browserRoots == nil {
				return nil, web.Problem{Status: 503, Code: "infrastructure.certificate_unavailable", Title: "Certificate storage or trust is unavailable"}
			}
			settings := config.InstallationSettings{}
			activation := config.Activation{ActorID: actor.User.ID, ActivatedAt: now()}
			if err := tx.QueryRowContext(ctx, `SELECT id,config_generation,organization_name,data_profile,
				management_origin,ide_host_pattern,preview_host_pattern,external_auth_interval_seconds
				FROM installations WHERE singleton=1 AND state='active'`).Scan(
				&activation.InstallationID, &activation.ExpectedGeneration, &settings.OrganizationName, &settings.DataProfile,
				&settings.ManagementOrigin, &settings.IDEHostPattern, &settings.PreviewHostPattern, &settings.ExternalAuthIntervalSeconds,
			); err != nil {
				return nil, err
			}
			privateKey := []byte(input.Key)
			defer clear(privateKey)
			certificateID := uuid.NewString()
			generation, err := config.ActivateBrowserCertificate(ctx, tx, activation, settings, config.BrowserCertificateInput{
				ID: certificateID, ChainPEM: []byte(input.Chain), KeyPEM: privateKey,
			}, s.cipher, s.browserRoots)
			if errors.Is(err, config.ErrInvalidBrowserCertificate) {
				return nil, web.Problem{Status: 400, Code: "infrastructure.invalid_certificate", Title: "Provide a trusted certificate and matching key for the service names"}
			}
			if err != nil {
				return nil, err
			}
			pair, err := config.LoadBrowserCertificate(ctx, tx, activation.InstallationID, certificateID, s.cipher, s.browserRoots, activation.ActivatedAt)
			if err != nil {
				return nil, err
			}
			op, err := journal.RecordLocalStepInTx(ctx, tx, operation.Request{
				Actor: actor.User.ID, Key: key, Type: "setup", OriginatingAction: "infrastructure.manage", Generation: generation,
				Steps: []operation.Step{{Key: replaceCertificateStep, Label: "Replace browser certificate"}},
			}, "Browser certificate replacement committed.")
			if err != nil {
				return nil, err
			}
			replacement := &servedBrowserCertificate{generation: generation, pair: pair}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The authorization wrapper invokes this only after commit.
				// Concurrent responses can arrive out of generation order.
				for current := s.browserReplacement.Load(); current == nil || current.generation < generation; current = s.browserReplacement.Load() {
					if s.browserReplacement.CompareAndSwap(current, replacement) {
						break
					}
				}
				certificateResponse(op).ServeHTTP(w, r)
			}), nil
		},
	}
}
