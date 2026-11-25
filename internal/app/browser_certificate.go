package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// browserCertificateReadHandler describes the leaf currently selected by the
// browser listener. It reads only public fields, even when validity has elapsed.
func (s *ControllerState) browserCertificateReadHandler(now func() time.Time) web.AuthorizedHandler {
	return web.AuthorizedHandler{
		DB: s.DB, Origin: s.Installation.Settings.ManagementOrigin, Now: now,
		Route: web.ManagementRoute{Method: http.MethodGet, Path: "/admin/certificate", OperationID: "getBrowserCertificate", Action: "infrastructure.view"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "infrastructure.view"}, nil
		},
		Apply: func(context.Context, *sql.Tx, *http.Request, auth.Principal, web.AuthorizationTarget, auth.Decision) (http.Handler, error) {
			pair := &s.BrowserTLS
			if current := s.browserReplacement.Load(); current != nil {
				pair = &current.pair
			}
			leaf := pair.Leaf
			if leaf == nil || len(pair.Certificate) == 0 {
				return nil, web.Problem{Status: 503, Code: "infrastructure.certificate_unavailable", Title: "The installed browser certificate is unavailable"}
			}
			status := "valid"
			currentTime := now()
			switch {
			case currentTime.Before(leaf.NotBefore):
				status = "not_yet_valid"
			case !currentTime.Before(leaf.NotAfter):
				status = "expired"
			case leaf.NotAfter.Sub(currentTime) <= 30*24*time.Hour:
				status = "expiring"
			}
			ips := make([]string, 0, len(leaf.IPAddresses))
			for _, ip := range leaf.IPAddresses {
				ips = append(ips, ip.String())
			}
			uris := make([]string, 0, len(leaf.URIs))
			for _, uri := range leaf.URIs {
				uris = append(uris, uri.String())
			}
			details := struct {
				Subject            string                `json:"subject"`
				Issuer             string                `json:"issuer"`
				DNSNames           []string              `json:"dnsNames"`
				IPAddresses        []string              `json:"ipAddresses"`
				EmailAddresses     []string              `json:"emailAddresses"`
				URIs               []string              `json:"uris"`
				NotBefore          time.Time             `json:"notBefore"`
				NotAfter           time.Time             `json:"notAfter"`
				SerialNumber       string                `json:"serialNumber"`
				FingerprintSHA256  string                `json:"fingerprintSha256"`
				SignatureAlgorithm string                `json:"signatureAlgorithm"`
				PublicKeyAlgorithm string                `json:"publicKeyAlgorithm"`
				Version            int                   `json:"version"`
				ChainLength        int                   `json:"chainLength"`
				Status             string                `json:"status"`
				Managed            bool                  `json:"managed,omitempty"`
				Renewal            *browserRenewalResult `json:"renewal,omitempty"`
			}{
				Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(),
				DNSNames: append([]string{}, leaf.DNSNames...), IPAddresses: ips,
				EmailAddresses: append([]string{}, leaf.EmailAddresses...), URIs: uris,
				NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(),
				SerialNumber: leaf.SerialNumber.Text(16), FingerprintSHA256: fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)),
				SignatureAlgorithm: leaf.SignatureAlgorithm.String(), PublicKeyAlgorithm: leaf.PublicKeyAlgorithm.String(),
				Version: leaf.Version, ChainLength: len(pair.Certificate), Status: status,
			}
			if s.browserACME != nil {
				details.Managed = true
				details.Renewal = s.browserACME.result()
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				_ = json.NewEncoder(w).Encode(details)
			}), nil
		},
	}
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
			if s.browserACME != nil {
				return nil, web.Problem{Status: 409, Code: "infrastructure.managed_certificate", Title: "Let's Encrypt manages the browser certificate"}
			}
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
			activation, settings, err := currentBrowserActivation(ctx, tx, actor.User.ID, now())
			if err != nil {
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
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The authorization wrapper invokes this only after commit.
				s.publishBrowserCertificate(generation, pair)
				certificateResponse(op).ServeHTTP(w, r)
			}), nil
		},
	}
}

func currentBrowserActivation(ctx context.Context, tx *sql.Tx, actor string, now time.Time) (config.Activation, config.InstallationSettings, error) {
	settings := config.InstallationSettings{}
	activation := config.Activation{ActorID: actor, ActivatedAt: now}
	err := tx.QueryRowContext(ctx, `SELECT id,config_generation,organization_name,data_profile,
		management_origin,ide_host_pattern,preview_host_pattern,external_auth_interval_seconds
		FROM installations WHERE singleton=1 AND state='active'`).Scan(
		&activation.InstallationID, &activation.ExpectedGeneration, &settings.OrganizationName, &settings.DataProfile,
		&settings.ManagementOrigin, &settings.IDEHostPattern, &settings.PreviewHostPattern, &settings.ExternalAuthIntervalSeconds,
	)
	return activation, settings, err
}

func (s *ControllerState) publishBrowserCertificate(generation int64, pair tls.Certificate) {
	// Concurrent administrator responses can arrive out of generation order.
	replacement := &servedBrowserCertificate{generation: generation, pair: pair}
	for current := s.browserReplacement.Load(); current == nil || current.generation < generation; current = s.browserReplacement.Load() {
		if s.browserReplacement.CompareAndSwap(current, replacement) {
			return
		}
	}
}
