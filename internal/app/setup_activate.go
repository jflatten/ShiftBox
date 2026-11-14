package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const activateStep = "activate_installation"

func (s SetupHTTP) activationHandler() http.Handler {
	return s.setupMethod(http.MethodPost, func(ctx context.Context, tx *sql.Tx, r *http.Request, principal auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		key, err := web.IdempotencyKey(r)
		if err != nil {
			return nil, err
		}
		op, err := s.activateSQLiteSetup(ctx, tx, principal.User.ID, key, s.Now())
		if errors.Is(err, ErrSetupConflict) || errors.Is(err, operation.ErrConflict) {
			return nil, web.Problem{Status: 409, Code: "setup.activation_unavailable", Title: "Complete installation validation before activating"}
		}
		if err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			web.ClearSessionCookie(w)
			writeSetupJSON(w, http.StatusAccepted, op)
		}), nil
	})
}

// activateSQLiteSetup changes runtime authority, retires bootstrap sessions,
// and records the completed activation in the caller's setup transaction.
func (s SetupHTTP) activateSQLiteSetup(ctx context.Context, tx *sql.Tx, actor, key string, now time.Time,
) (operation.Operation, error) {
	var postgresSelected bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM protected_values WHERE id=$1)",
		s.pendingPostgresContext().RecordID).Scan(&postgresSelected); err != nil {
		return operation.Operation{}, err
	}
	if postgresSelected {
		return operation.Operation{}, ErrSetupConflict
	}
	installationID := s.InstallationID
	var generation int64
	var snapshot string
	err := tx.QueryRowContext(ctx, `SELECT i.config_generation,a.settings_json FROM installations i
		JOIN bootstrap_administrator b ON b.installation_id=i.id
		JOIN users u ON u.id=b.user_id
		JOIN activated_configurations a ON a.installation_id=i.id AND a.generation=i.config_generation AND a.retired_at IS NULL
		JOIN capacity_budgets budget ON budget.installation_id=i.id
		WHERE i.id=$1 AND i.state='configuring' AND i.data_profile='sqlite' AND i.config_generation>0
		  AND b.user_id=$2 AND b.credential_ready=1 AND b.delivered_at IS NOT NULL AND b.password_ciphertext_id IS NULL
		  AND u.role='administrator' AND u.membership_state='active' AND u.credential_mode='local' AND u.must_change_password=0
		  AND EXISTS (SELECT 1 FROM platform_artifacts WHERE state='available' AND is_default=1
		    AND protocol_version='1' AND guest_release IN (i.active_release,i.min_guest_release))
		  AND EXISTS (SELECT 1 FROM operations o JOIN operation_steps step ON step.operation_id=o.id
		    WHERE o.requested_by=$2 AND o.type='setup' AND o.status='succeeded'
		    AND step.step_key=$3 AND step.status='succeeded')`,
		installationID, actor, profileStep).Scan(&generation, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, ErrSetupConflict
	}
	if err != nil {
		return operation.Operation{}, err
	}
	var settings config.InstallationSettings
	if json.Unmarshal([]byte(snapshot), &settings) != nil || settings.DataProfile != "sqlite" ||
		settings.BrowserCertificateID == "" || s.CertificateRoots == nil {
		return operation.Operation{}, ErrSetupConflict
	}
	if _, err := config.LoadBrowserCertificate(ctx, tx, installationID, settings.BrowserCertificateID, s.Cipher, s.CertificateRoots, now); err != nil {
		return operation.Operation{}, ErrSetupConflict
	}
	op, err := s.Journal.RecordLocalStepInTx(ctx, tx, operation.Request{
		Type: "setup", OriginatingAction: "setup.mutate", Actor: actor, Key: key, Generation: generation,
		Steps: []operation.Step{{Key: activateStep, Label: "Activate installation"}},
	}, "Installation activated. Sign in again to continue.")
	if err != nil {
		return operation.Operation{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE installations SET state='active',updated_at=$2,version=version+1
		WHERE id=$1 AND state='configuring' AND data_profile='sqlite' AND config_generation=$3`, installationID, stamp, generation)
	if err != nil {
		return operation.Operation{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return operation.Operation{}, err
	}
	if count != 1 {
		return operation.Operation{}, ErrSetupConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason='setup_activated'
		WHERE mode IN ('bootstrap_restricted','password_change') AND revoked_at IS NULL`, stamp)
	if err != nil {
		return operation.Operation{}, err
	}
	return op, nil
}
