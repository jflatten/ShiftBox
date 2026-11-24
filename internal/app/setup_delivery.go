package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
)

// MarkAdministratorReady records successful Dex reconciliation only for the
// credential intent that was sent. A stale result cannot ready a reset password.
func MarkAdministratorReady(ctx context.Context, tx *sql.Tx, installationID, userID string, expectedHash []byte) error {
	result, err := tx.ExecContext(ctx, `UPDATE bootstrap_administrator SET credential_ready=1
		WHERE installation_id=$1 AND user_id=$2 AND password_hash=$3
		AND EXISTS(SELECT 1 FROM installations i WHERE i.id=$1 AND i.state IN ('bootstrap','configuring'))
		AND EXISTS(SELECT 1 FROM users u WHERE u.id=$2 AND u.membership_state='active'
		AND u.role='administrator' AND u.credential_mode='local'
		AND (u.must_change_password=1 OR (credential_ready=0 AND delivered_at IS NOT NULL AND password_ciphertext_id IS NULL)))`,
		installationID, userID, string(expectedHash))
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

// ClaimTemporaryPassword returns plaintext only after its one-time delivery claim
// and ciphertext removal commit. Call only from the protected local setup path.
// A lost response requires the explicit local reset flow; ordinary retry cannot
// redisplay or reset the credential. Failure never returns a plaintext value.
func ClaimTemporaryPassword(ctx context.Context, db *sql.DB, installationID string, cipher *cryptostore.Store, now time.Time) (string, error) {
	if cipher == nil || now.IsZero() {
		return "", ErrSetupConflict
	}
	var password []byte
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var userID, valueID, encoded string
		err := tx.QueryRowContext(ctx, `SELECT b.user_id,b.password_ciphertext_id,p.value_json FROM bootstrap_administrator b JOIN installations i ON i.id=b.installation_id JOIN users u ON u.id=b.user_id JOIN protected_values p ON p.id=b.password_ciphertext_id WHERE b.installation_id=$1 AND b.credential_ready=1 AND b.delivered_at IS NULL AND i.state IN ('bootstrap','configuring') AND u.must_change_password=1 AND u.membership_state='active' AND u.role='administrator'`, installationID).Scan(&userID, &valueID, &encoded)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSetupConflict
		}
		if err != nil {
			return err
		}
		var value cryptostore.Value
		if err := json.Unmarshal([]byte(encoded), &value); err != nil {
			return ErrSetupConflict
		}
		password, err = cipher.Open(bootstrapPasswordContext(installationID, userID), value)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE bootstrap_administrator SET delivered_at=$1,password_ciphertext_id=NULL WHERE installation_id=$2 AND delivered_at IS NULL AND password_ciphertext_id=$3`, now.UTC().Format(time.RFC3339Nano), installationID, valueID)
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM protected_values WHERE id=$1`, valueID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return string(password), nil
}
