package backup

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

var errRecoveryState = errors.New("cannot enter recovery review for the restored installation")

// EnterRecovery invalidates copied authority and opens an identity review
// before the verified replacement database becomes authoritative.
func EnterRecovery(ctx context.Context, db *sql.DB, journal *operation.Journal,
	installationID, validationID, actor string, now time.Time) (string, error) {
	return EnterRecoveryWithConfiguration(ctx, db, journal, installationID, validationID, actor, now, nil)
}

// EnterRecoveryWithConfiguration replaces profile-specific configuration in
// the same transaction that creates the recovery gate and revokes authority.
func EnterRecoveryWithConfiguration(ctx context.Context, db *sql.DB, journal *operation.Journal,
	installationID, validationID, actor string, now time.Time, configure func(context.Context, *sql.Tx) error) (string, error) {
	if id, err := uuid.Parse(validationID); err != nil || id.String() != validationID || actor == "" || now.IsZero() {
		return "", errRecoveryState
	}
	var reviewID string
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		stamp := now.UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE installations SET state='recovery',version=version+1,updated_at=$1
			WHERE singleton=1 AND id=$2 AND state='active'`, stamp, installationID)
		if err := changedRecord(result, err); err != nil {
			return err
		}
		if configure != nil {
			if err := configure(ctx, tx); err != nil {
				return err
			}
		}
		if err := invalidateRestoredAuthority(ctx, tx, stamp); err != nil {
			return err
		}
		if err := interruptRestoredOperations(ctx, tx, journal); err != nil {
			return err
		}
		op, err := journal.RecordLocalStepInTx(ctx, tx, operation.Request{
			Actor: actor, Key: validationID, Type: "restore", Generation: 1, OriginatingAction: "restore.activate",
			Steps: []operation.Step{{Key: "restore", Label: "Enter recovery review"}},
		}, "The recovered installation is ready for identity review.")
		if err != nil {
			return err
		}
		reviewID = uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO recovery_reviews(id,restore_operation_id,state,started_by,started_at,version)
			VALUES($1,$2,'identity_review',$3,$4,1)`, reviewID, op.ID, actor, stamp)
		return err
	})
	if err != nil {
		return "", errRecoveryState
	}
	return reviewID, nil
}

func invalidateRestoredAuthority(ctx context.Context, tx *sql.Tx, stamp string) error {
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE sessions SET revoked_at=$1,revocation_reason='restore' WHERE revoked_at IS NULL`, []any{stamp}},
		{`UPDATE origin_grants SET revoked_at=$1 WHERE revoked_at IS NULL`, []any{stamp}},
		{`UPDATE enrollment_grants SET state='invalidated' WHERE state IN ('issued','consumed')`, nil},
		{`DELETE FROM github_oauth_states`, nil},
		{`UPDATE github_authorizations SET state='reconnect_required',nonce=$1,ciphertext=$1,
			access_expires_at='',refresh_expires_at='',updated_at=$2,version=version+1
			WHERE state IN ('connected','refreshing')`, []any{[]byte{}, stamp}},
		{`UPDATE backup_records SET state='failed',error_code='backup.interrupted'
			WHERE state IN ('preparing','checkpointing','encrypting')`, nil},
		{`UPDATE maintenance_campaigns SET state='cancelled' WHERE state IN ('scheduled','active','blocked')`, nil},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func interruptRestoredOperations(ctx context.Context, tx *sql.Tx, journal *operation.Journal) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM operations WHERE status IN ('pending','running','waiting')")
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := journal.InterruptInTx(ctx, tx, id, "Recovery requires a fresh request after review."); err != nil {
			return err
		}
	}
	return nil
}
