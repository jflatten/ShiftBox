package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

type setupSnapshot struct {
	id, state, profile, organization, origin, ide, preview             string
	release, minGuest, created, updated                                string
	interval, generation, version                                      int64
	owner                                                              auth.User
	ownerCreator, ownerCreated, ownerUpdated                           string
	settings, activatedBy, activatedAt                                 string
	certID, chain, keyID, notBefore, notAfter, fingerprint, ciphertext string
}

// StagePostgresSetup copies the selected setup records to the migrated target.
// Both stores remain inactive for production traffic. The caller owns Dex
// transfer and the separate durable authority switch.
func StagePostgresSetup(ctx context.Context, source, target *sql.DB, installationID string, now time.Time) (auth.User, error) {
	var staged setupSnapshot
	err := database.Transaction(ctx, source, func(tx *sql.Tx) error {
		var ownerID string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM bootstrap_administrator WHERE installation_id=$1
			AND credential_ready=1 AND delivered_at IS NOT NULL AND password_ciphertext_id IS NULL`, installationID).Scan(&ownerID); err != nil {
			return err
		}
		var err error
		staged, err = readSetupSnapshot(ctx, tx, installationID, ownerID)
		return err
	})
	if err != nil || now.IsZero() || staged.state != "configuring" || staged.profile != "sqlite" {
		return auth.User{}, ErrSetupConflict
	}
	var settings config.InstallationSettings
	if err := json.Unmarshal([]byte(staged.settings), &settings); err != nil || settings.BrowserCertificateID != staged.certID {
		return auth.User{}, ErrSetupConflict
	}
	settings.DataProfile = "postgres"
	encoded, err := json.Marshal(settings)
	if err != nil {
		return auth.User{}, err
	}
	err = database.Transaction(ctx, target, func(tx *sql.Tx) error {
		var existingID string
		err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&existingID)
		if err == nil {
			if existingID != staged.id {
				return ErrSetupConflict
			}
			existing, err := readSetupSnapshot(ctx, tx, staged.id, staged.owner.ID)
			expected := staged
			expected.profile = "postgres"
			expected.settings = string(encoded)
			expected.version++
			// The original staging timestamp is retained across retries.
			expected.updated = existing.updated
			if err != nil || !reflect.DeepEqual(existing, expected) {
				return ErrSetupConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return staged.insert(ctx, tx, string(encoded), now)
	})
	if err != nil {
		return auth.User{}, err
	}
	return staged.owner, nil
}

func readSetupSnapshot(ctx context.Context, tx *sql.Tx, id, ownerID string) (setupSnapshot, error) {
	var s setupSnapshot
	err := tx.QueryRowContext(ctx, `SELECT id,state,data_profile,organization_name,management_origin,ide_host_pattern,
		preview_host_pattern,external_auth_interval_seconds,active_release,min_guest_release,config_generation,created_at,updated_at,version
		FROM installations WHERE id=$1`, id).Scan(&s.id, &s.state, &s.profile, &s.organization, &s.origin, &s.ide, &s.preview,
		&s.interval, &s.release, &s.minGuest, &s.generation, &s.created, &s.updated, &s.version)
	if err != nil {
		return s, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id,primary_email,display_name,role,membership_state,credential_mode,
		created_by,created_at,updated_at,version FROM users WHERE id=$1 AND role='administrator'
		AND membership_state='active' AND credential_mode='local' AND must_change_password=0
		AND (SELECT COUNT(*) FROM users)=1`, ownerID).Scan(&s.owner.ID, &s.owner.PrimaryEmail, &s.owner.DisplayName,
		&s.owner.Role, &s.owner.MembershipState, &s.owner.CredentialMode, &s.ownerCreator, &s.ownerCreated, &s.ownerUpdated, &s.owner.Version)
	if err != nil {
		return s, err
	}
	err = tx.QueryRowContext(ctx, `SELECT settings_json,activated_by,activated_at FROM activated_configurations
		WHERE installation_id=$1 AND generation=$2 AND retired_at IS NULL`, id, s.generation).Scan(&s.settings, &s.activatedBy, &s.activatedAt)
	if err != nil {
		return s, err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.chain_pem,c.private_key_ciphertext_id,c.not_before,c.not_after,c.fingerprint_sha256,p.value_json
		FROM browser_certificates c JOIN protected_values p ON p.id=c.private_key_ciphertext_id WHERE c.state='active'`).Scan(
		&s.certID, &s.chain, &s.keyID, &s.notBefore, &s.notAfter, &s.fingerprint, &s.ciphertext)
	return s, err
}

func (s setupSnapshot) insert(ctx context.Context, tx *sql.Tx, settings string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO installations(id,state,data_profile,organization_name,management_origin,ide_host_pattern,
		preview_host_pattern,external_auth_interval_seconds,active_release,min_guest_release,config_generation,created_at,updated_at,version)
		VALUES($1,'configuring','postgres',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		s.id, s.organization, s.origin, s.ide, s.preview, s.interval, s.release, s.minGuest, s.generation, s.created, now.UTC().Format(time.RFC3339Nano), s.version+1); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,primary_email,display_name,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,$2,$3,'administrator','active','local',$4,$5,$6,$7)`, s.owner.ID, s.owner.PrimaryEmail, s.owner.DisplayName,
		s.ownerCreator, s.ownerCreated, s.ownerUpdated, s.owner.Version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO protected_values(id,value_json) VALUES($1,$2)`, s.keyID, s.ciphertext); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_certificates(id,chain_pem,private_key_ciphertext_id,not_before,not_after,fingerprint_sha256,state)
		VALUES($1,$2,$3,$4,$5,$6,'active')`, s.certID, s.chain, s.keyID, s.notBefore, s.notAfter, s.fingerprint); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at)
		VALUES($1,$2,$3,$4,$5)`, s.id, s.generation, settings, s.activatedBy, s.activatedAt)
	return err
}
