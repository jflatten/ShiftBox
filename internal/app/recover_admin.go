package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
)

// ErrAdministratorRecoveryDenied indicates the local account is ineligible.
var ErrAdministratorRecoveryDenied = errors.New("recovery requires an existing active local administrator")

// OpenAdministratorRecovery validates existing local state and loads only the
// database and private Dex configuration needed for offline recovery.
func OpenAdministratorRecovery(ctx context.Context, cfg config.Config) (*ControllerState, error) {
	cipher, err := loadControllerCipher(cfg.InstallationDir)
	if err != nil {
		return nil, err
	}
	db, active, err := OpenActiveDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	state := &ControllerState{DB: db, Installation: active}
	if cfg.DataProfile == "postgres" {
		state.DexPostgres, err = loadPostgresDexStorage(ctx, db, active.InstallationID, cfg.PostgresURI, cipher)
		if err != nil {
			db.Close()
			return nil, err
		}
	}
	return state, nil
}

// RecoverLocalAdministrator resets an existing active local administrator.
// The controller must be stopped and the caller must hold the installation lock.
func (s *ControllerState) RecoverLocalAdministrator(ctx context.Context, email string, admin identity.PasswordAdmin) (string, error) {
	return recoverLocalAdministrator(ctx, s.DB, s.Installation.InstallationID, email, admin, time.Now())
}

type passwordHashResetter interface {
	ResetPasswordHash(context.Context, string, string, []byte) error
}

func recoverLocalAdministrator(ctx context.Context, db *sql.DB, installation, email string, dex passwordHashResetter, now time.Time) (string, error) {
	var userID string
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT u.id FROM users u CROSS JOIN installations i
			WHERE i.id=$1 AND i.state='active' AND u.primary_email=$2 AND u.role='administrator'
			AND u.membership_state='active' AND u.credential_mode='local'
			AND (SELECT count(*) FROM users WHERE primary_email=$2 AND credential_mode='local')=1`, installation, email).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAdministratorRecoveryDenied
		}
		if err != nil {
			return err
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, "UPDATE users SET must_change_password=1,updated_at=$1,version=version+1 WHERE id=$2", stamp, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE origin_grants SET revoked_at=$1 WHERE user_id=$2 AND revoked_at IS NULL", stamp, userID); err != nil {
			return err
		}
		if err := auth.NewStore(db).RevokeUserSessions(ctx, tx, userID, now, "administrator_recovery"); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "local_recovery", ActorID: userID,
			Action: "user.recover_admin", ResourceType: "user", ResourceID: userID, Outcome: "started", ReasonCode: "sessions_revoked"})
		return err
	})
	if err != nil {
		return "", err
	}
	password, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		return "", err
	}
	defer clear(hash)
	if err := dex.ResetPasswordHash(ctx, email, userID, hash); err != nil {
		return "", err
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = database.Transaction(finish, db, func(tx *sql.Tx) error {
		_, err := audit.Append(finish, tx, audit.Event{OccurredAt: time.Now(), ActorType: "local_recovery", ActorID: userID,
			Action: "user.recover_admin", ResourceType: "user", ResourceID: userID, Outcome: "allowed", ReasonCode: "temporary_password_reissued"})
		return err
	})
	if err != nil {
		return "", err
	}
	return password, nil
}
