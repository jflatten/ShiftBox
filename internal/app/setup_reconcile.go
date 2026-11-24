package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jflatten/ShiftBox/internal/database"
)

// PasswordAdministrator is the idempotent, authenticated Dex creation boundary.
// Ensure must preserve an existing credential and reject a different stable ID.
type PasswordAdministrator interface {
	Ensure(context.Context, string, string, []byte) error
}

// ReconcileAdministrator reads committed setup intent, calls Dex outside the
// database transaction, then fences readiness against the same credential hash.
// A failed/ambiguous API call leaves delivery blocked and may be retried safely.
// The caller supplies a bounded operation context and protected setup admission.
func ReconcileAdministrator(ctx context.Context, db *sql.DB, installationID string, admin PasswordAdministrator) error {
	if db == nil || admin == nil {
		return ErrSetupConflict
	}
	var record AdministratorRecord
	var ready bool
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		// A chosen password is recorded as delivered before Dex reconciliation.
		err := tx.QueryRowContext(ctx, `SELECT u.id,u.primary_email,b.password_hash,b.credential_ready
			FROM bootstrap_administrator b JOIN installations i ON i.id=b.installation_id JOIN users u ON u.id=b.user_id
			WHERE b.installation_id=$1 AND i.state IN ('bootstrap','configuring')
			AND u.membership_state='active' AND u.role='administrator' AND u.credential_mode='local'
			AND (u.must_change_password=1 OR (b.credential_ready=0 AND b.delivered_at IS NOT NULL AND b.password_ciphertext_id IS NULL))`,
			installationID).Scan(&record.UserID, &record.Email, &record.PasswordHash, &ready)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		return err
	})
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if err := admin.Ensure(ctx, record.Email, record.UserID, record.PasswordHash); err != nil {
		return err
	}
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return MarkAdministratorReady(ctx, tx, installationID, record.UserID, record.PasswordHash)
	})
}
