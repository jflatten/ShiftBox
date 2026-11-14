package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const certificateStep = "validate_browser_certificate"

func (s SetupHTTP) certificateHandler() http.Handler {
	return s.setupMutation(func(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		key, err := web.IdempotencyKey(r)
		if err != nil {
			return nil, err
		}
		var input struct {
			Chain      string `json:"certificateChainPem"`
			Key        string `json:"privateKeyPem"`
			Management string `json:"managementOrigin"`
			IDE        string `json:"ideHostPattern"`
			Preview    string `json:"previewHostPattern"`
		}
		if err := web.DecodeJSON(nil, r, &input, 256<<10); err != nil {
			return nil, err
		}
		var selected bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM operations o JOIN operation_steps step ON step.operation_id=o.id
			WHERE o.requested_by=$1 AND o.type='setup' AND o.status='succeeded'
			AND step.step_key=$2 AND step.status='succeeded'
		)`, p.User.ID, profileStep).Scan(&selected); err != nil {
			return nil, err
		}
		if !selected || p.InstallationState != "configuring" {
			return nil, web.Problem{Status: 409, Code: "setup.profile_required", Title: "Select a data profile first"}
		}

		var previousID string
		err = tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", p.User.ID, key).Scan(&previousID)
		switch {
		case err == nil:
			op, err := s.Journal.GetInTx(ctx, tx, previousID)
			if err != nil {
				return nil, err
			}
			if op.Type != "setup" || op.OriginatingAction != "setup.mutate" || op.Status != "succeeded" ||
				len(op.Steps) != 1 || op.Steps[0].Key != certificateStep {
				return nil, web.Problem{Status: 409, Code: "setup.operation_conflict", Title: "Use a new request key"}
			}
			return certificateResponse(op), nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}

		if s.CertificateRoots == nil {
			return nil, web.Problem{Status: 503, Code: "setup.certificate_trust_unavailable", Title: "Certificate trust is unavailable"}
		}
		settings := config.InstallationSettings{ManagementOrigin: input.Management, IDEHostPattern: input.IDE, PreviewHostPattern: input.Preview}
		if _, err := config.NewServiceHosts(input.Management, input.IDE, input.Preview); err != nil {
			return nil, web.Problem{Status: 400, Code: "setup.invalid_service_names", Title: "Correct the management and service names"}
		}
		activation := config.Activation{InstallationID: s.InstallationID, ActorID: p.User.ID, ActivatedAt: s.Now()}
		if err := tx.QueryRowContext(ctx, `SELECT organization_name,data_profile,external_auth_interval_seconds,config_generation
			FROM installations WHERE id=$1`, s.InstallationID).Scan(&settings.OrganizationName, &settings.DataProfile,
			&settings.ExternalAuthIntervalSeconds, &activation.ExpectedGeneration); err != nil {
			return nil, err
		}
		generation, err := config.ActivateBrowserCertificate(ctx, tx, activation, settings, config.BrowserCertificateInput{
			ID: s.NewID(), ChainPEM: []byte(input.Chain), KeyPEM: []byte(input.Key),
		}, s.Cipher, s.CertificateRoots)
		if errors.Is(err, config.ErrInvalidBrowserCertificate) {
			return nil, web.Problem{Status: 400, Code: "setup.invalid_certificate", Title: "Provide a trusted certificate and matching key for the service names"}
		}
		if err != nil {
			return nil, err
		}
		op, err := s.Journal.RecordLocalStepInTx(ctx, tx, operation.Request{
			Actor: p.User.ID, Key: key, Type: "setup", OriginatingAction: "setup.mutate", Generation: generation,
			Steps: []operation.Step{{Key: certificateStep, Label: "Validate browser certificate"}},
		}, "Browser certificate and service names validated.")
		if err != nil {
			return nil, err
		}
		return certificateResponse(op), nil
	})
}

func certificateResponse(op operation.Operation) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSetupJSON(w, http.StatusAccepted, op)
	})
}
