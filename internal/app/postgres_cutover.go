package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const retireBootstrapStep = "retire_bootstrap_database"

// ResumePostgresActivation reads the durable source receipt and completes the
// same cutover. Call from the protected local command with the installation lock.
func (s SetupHTTP) ResumePostgresActivation(ctx context.Context, target *sql.DB) (operation.Operation, error) {
	var actor, key string
	err := s.DB.QueryRowContext(ctx, `SELECT o.requested_by,o.request_key FROM installations i
		JOIN bootstrap_administrator b ON b.installation_id=i.id
		JOIN operations o ON o.requested_by=b.user_id AND o.generation=i.config_generation
		JOIN operation_steps step ON step.operation_id=o.id
		WHERE i.id=$1 AND i.state='blocked' AND i.data_profile='sqlite' AND o.type='setup'
		  AND o.originating_action='setup.mutate' AND o.status='succeeded'
		  AND step.step_key=$2 AND step.status='succeeded'`, s.InstallationID, retireBootstrapStep).Scan(&actor, &key)
	if err != nil {
		return operation.Operation{}, ErrSetupConflict
	}
	return s.ActivatePostgres(ctx, target, actor, key)
}

// ActivatePostgres switches a staged installation to PostgreSQL. The caller
// must authenticate the local owner and prove the changed Dex credential was
// transferred before invoking it. A restart resumes with the same request key.
func (s SetupHTTP) ActivatePostgres(ctx context.Context, target *sql.DB, actor, key string) (operation.Operation, error) {
	return s.activatePostgres(ctx, target, actor, key, "", "")
}

func (s SetupHTTP) activatePostgres(ctx context.Context, target *sql.DB, actor, key, profileKey, token string) (operation.Operation, error) {
	if s.DB == nil || target == nil || s.DB == target || s.Journal == nil || s.Now == nil || s.NewID == nil ||
		s.Cipher == nil || s.CertificateRoots == nil {
		return operation.Operation{}, ErrSetupConflict
	}
	if err := database.CheckSchema(ctx, target, "postgres"); err != nil {
		return operation.Operation{}, ErrSetupConflict
	}
	var budgetVersion int64
	var targetArtifact *setupArtifact
	if profileKey != "" {
		if err := target.QueryRowContext(ctx, "SELECT version FROM capacity_budgets WHERE installation_id=$1", s.InstallationID).Scan(&budgetVersion); err != nil {
			return operation.Operation{}, ErrSetupConflict
		}
		if s.Cloud == nil {
			var err error
			targetArtifact, err = readSetupArtifact(ctx, target)
			if err != nil || targetArtifact == nil {
				return operation.Operation{}, ErrSetupConflict
			}
		}
	}
	c := postgresCutover{source: s.Journal, target: operation.NewJournal(target, s.Now, s.NewID),
		id: s.InstallationID, actor: actor, key: key, now: s.Now(),
		verifySource: func(ctx context.Context, tx *sql.Tx) error {
			if profileKey == "" {
				return nil
			}
			selected, err := s.readPendingPostgres(ctx, tx)
			if err != nil || selected.RequestKey != profileKey {
				return ErrSetupConflict
			}
			var currentBudget int64
			if err := tx.QueryRowContext(ctx, "SELECT version FROM capacity_budgets WHERE installation_id=$1", s.InstallationID).Scan(&currentBudget); err != nil ||
				currentBudget != budgetVersion {
				return ErrSetupConflict
			}
			if s.Cloud == nil {
				currentArtifact, err := readSetupArtifact(ctx, tx)
				if err != nil || currentArtifact == nil || *currentArtifact != *targetArtifact {
					return ErrSetupConflict
				}
			}
			principal, err := s.bootstrapPrincipal(ctx, tx, token)
			if err != nil || principal.User.ID != actor || !auth.Authorize(principal, "setup.mutate", auth.Resource{}, nil, s.Now()).Allowed {
				return ErrSetupConflict
			}
			return nil
		}}
	c.validate = func(ctx context.Context, tx *sql.Tx) error {
		var snapshot string
		err := tx.QueryRowContext(ctx, `SELECT a.settings_json FROM installations i
			JOIN activated_configurations a ON a.installation_id=i.id AND a.generation=i.config_generation AND a.retired_at IS NULL
			JOIN capacity_budgets budget ON budget.installation_id=i.id
			JOIN users u ON u.id=$2
			WHERE i.id=$1 AND i.state='configuring' AND i.data_profile='postgres'
			  AND u.role='administrator' AND u.membership_state='active' AND u.credential_mode='local' AND u.must_change_password=0
			  AND (SELECT COUNT(*) FROM users)=1 AND NOT EXISTS(SELECT 1 FROM sessions)
			  AND ($3 OR EXISTS(SELECT 1 FROM platform_artifacts WHERE state='available' AND is_default=1
			    AND protocol_version='1' AND guest_release IN (i.active_release,i.min_guest_release)))`, c.id, actor, s.Cloud != nil).Scan(&snapshot)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		if err != nil {
			return err
		}
		var settings config.InstallationSettings
		if json.Unmarshal([]byte(snapshot), &settings) != nil || settings.DataProfile != "postgres" || settings.BrowserCertificateID == "" {
			return ErrSetupConflict
		}
		if _, err := config.LoadBrowserCertificate(ctx, tx, c.id, settings.BrowserCertificateID, s.Cipher, s.CertificateRoots, c.now); err != nil {
			return ErrSetupConflict
		}
		if _, err := readDexStorage(ctx, tx, dexStorageContext(c.id), s.Cipher); err != nil {
			return ErrSetupConflict
		}
		return nil
	}
	return c.run(ctx, s.DB, target)
}

type postgresCutover struct {
	source, target *operation.Journal
	id, actor, key string
	now            time.Time
	validate       func(context.Context, *sql.Tx) error
	verifySource   func(context.Context, *sql.Tx) error
}

func (c postgresCutover) run(ctx context.Context, source, target *sql.DB) (operation.Operation, error) {
	// First give the prepared target a durable operation identity. The source
	// receipt binds retirement to this exact operation, even across restarts.
	prepared, err := c.prepare(ctx, target)
	if err != nil {
		return operation.Operation{}, err
	}
	if err := c.retireSource(ctx, source, prepared); err != nil {
		return operation.Operation{}, err
	}
	var activated operation.Operation
	err = database.Transaction(ctx, target, func(tx *sql.Tx) error {
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT state FROM installations
			WHERE id=$1 AND data_profile='postgres' AND config_generation=$2`, c.id, prepared.Generation).Scan(&state); err != nil {
			return err
		}
		current, err := c.target.GetInTx(ctx, tx, prepared.ID)
		if err != nil {
			return err
		}
		switch {
		case state == "active" && current.Status == "succeeded":
			activated = current
			return nil
		case state == "configuring" && current.Status == "pending":
			if err := c.validate(ctx, tx); err != nil {
				return err
			}
		default:
			return ErrSetupConflict
		}
		activated, err = c.target.RecordLocalStepInTx(ctx, tx, c.request(activateStep, "Activate PostgreSQL installation", prepared.Generation),
			"Installation activated. Sign in again to continue.")
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE installations SET state='active',updated_at=$3,version=version+1
			WHERE id=$1 AND config_generation=$2 AND state='configuring' AND data_profile='postgres'`,
			c.id, prepared.Generation, c.now.UTC().Format(time.RFC3339Nano))
		return checkCutoverWrite(result, err)
	})
	if err != nil {
		return operation.Operation{}, err
	}
	return activated, nil
}

func (c postgresCutover) prepare(ctx context.Context, target *sql.DB) (operation.Operation, error) {
	var prepared operation.Operation
	err := database.Transaction(ctx, target, func(tx *sql.Tx) error {
		var state string
		var generation int64
		err := tx.QueryRowContext(ctx, `SELECT state,config_generation FROM installations
			WHERE id=$1 AND data_profile='postgres' AND state IN ('configuring','active') AND config_generation>0`, c.id).Scan(&state, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		if err != nil {
			return err
		}
		if state == "configuring" {
			if err := c.validate(ctx, tx); err != nil {
				return err
			}
		}
		prepared, err = c.target.CreateInTx(ctx, tx, c.request(activateStep, "Activate PostgreSQL installation", generation))
		if err != nil {
			return err
		}
		if prepared.Type != "setup" || prepared.OriginatingAction != "setup.mutate" || prepared.Generation != generation ||
			len(prepared.Steps) != 1 || prepared.Steps[0].Key != activateStep ||
			(state == "active" && prepared.Status != "succeeded") || (state == "configuring" && prepared.Status != "pending") {
			return ErrSetupConflict
		}
		return nil
	})
	return prepared, err
}

func (c postgresCutover) retireSource(ctx context.Context, source *sql.DB, target operation.Operation) error {
	return database.Transaction(ctx, source, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT i.state FROM installations i
			JOIN bootstrap_administrator b ON b.installation_id=i.id
			JOIN users u ON u.id=b.user_id
			WHERE i.id=$1 AND i.data_profile='sqlite' AND i.config_generation=$2 AND b.user_id=$3
			  AND b.credential_ready=1 AND b.delivered_at IS NOT NULL AND b.password_ciphertext_id IS NULL
			  AND u.role='administrator' AND u.membership_state='active' AND u.credential_mode='local' AND u.must_change_password=0`,
			c.id, target.Generation, c.actor).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		if err != nil {
			return err
		}
		evidence := "Bootstrap retired for PostgreSQL activation " + target.ID
		request := c.request(retireBootstrapStep, "Retire local setup sessions", target.Generation)
		if state == "blocked" {
			var matching bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations o JOIN operation_steps s ON s.operation_id=o.id
				WHERE o.requested_by=$1 AND o.request_key=$2 AND o.generation=$3 AND o.type='setup' AND o.status='succeeded'
				  AND s.step_key=$4 AND s.status='succeeded' AND s.detail=$5)`,
				c.actor, c.key, target.Generation, retireBootstrapStep, evidence).Scan(&matching)
			if err != nil {
				return err
			}
			if !matching {
				return ErrSetupConflict
			}
			return nil
		}
		if state != "configuring" || target.Status != "pending" {
			return ErrSetupConflict
		}
		if c.verifySource != nil {
			if err := c.verifySource(ctx, tx); err != nil {
				return err
			}
		}
		stamp := c.now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE installations SET state='blocked',updated_at=$3,version=version+1
			WHERE id=$1 AND config_generation=$2 AND state='configuring' AND data_profile='sqlite'`, c.id, target.Generation, stamp)
		if err := checkCutoverWrite(result, err); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason='setup_activated'
			WHERE revoked_at IS NULL`, stamp); err != nil {
			return err
		}
		_, err = c.source.RecordLocalStepInTx(ctx, tx, request, evidence)
		return err
	})
}

func (c postgresCutover) request(step, label string, generation int64) operation.Request {
	return operation.Request{Type: "setup", OriginatingAction: "setup.mutate", Actor: c.actor, Key: c.key, Generation: generation,
		Steps: []operation.Step{{Key: step, Label: label}}}
}

func checkCutoverWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrSetupConflict
	}
	return nil
}
