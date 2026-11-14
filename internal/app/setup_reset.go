package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
)

// ResetSetupTemporaryPassword reissues a delivered initial credential while
// local setup still requires the initial password change. The caller must hold
// the installation's exclusive local setup lock.
func ResetSetupTemporaryPassword(ctx context.Context, db *sql.DB, email string, admin identity.PasswordAdmin, now func() time.Time) (string, error) {
	var installation, user string
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT b.installation_id,u.id FROM bootstrap_administrator b
			JOIN installations i ON i.id=b.installation_id JOIN users u ON u.id=b.user_id
			WHERE i.singleton=1 AND i.data_profile='sqlite' AND i.state IN ('bootstrap','configuring')
			AND b.delivered_at IS NOT NULL AND b.password_ciphertext_id IS NULL
			AND EXISTS (SELECT 1 FROM operations o WHERE o.id=b.operation_id AND o.status='succeeded')
			AND u.primary_email=$1 AND u.role='administrator' AND u.membership_state='active'
			AND u.credential_mode='local' AND u.must_change_password=1`, email).Scan(&installation, &user)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bootstrap_administrator SET credential_ready=0 WHERE installation_id=$1", installation); err != nil {
			return err
		}
		return auth.NewStore(db).RevokeUserSessions(ctx, tx, user, now(), "temporary_password_reset")
	})
	if err != nil {
		return "", err
	}
	password, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		return "", err
	}
	defer clear(hash)
	if err := admin.ResetPasswordHash(ctx, email, user, hash); err != nil {
		return "", err
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = database.Transaction(finish, db, func(tx *sql.Tx) error {
		stamp := now().UTC()
		result, err := tx.ExecContext(finish, `UPDATE bootstrap_administrator SET password_hash=$1,credential_ready=1,delivered_at=$2
			WHERE installation_id=$3 AND user_id=$4 AND credential_ready=0
			AND EXISTS (SELECT 1 FROM installations WHERE id=$3 AND state IN ('bootstrap','configuring'))
			AND EXISTS (SELECT 1 FROM users WHERE id=$4 AND must_change_password=1 AND role='administrator' AND membership_state='active')`,
			string(hash), stamp.Format(time.RFC3339Nano), installation, user)
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
		_, err = audit.Append(finish, tx, audit.Event{OccurredAt: stamp, ActorType: "local_recovery", ActorID: user,
			Action: "setup.reset_temporary_password", ResourceType: "user", ResourceID: user, Outcome: "allowed", ReasonCode: "temporary_password_reissued"})
		return err
	})
	if err != nil {
		return "", err
	}
	return password, nil
}
