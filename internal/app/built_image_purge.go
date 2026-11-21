package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/imagecache"
)

const unreferencedBuiltAttempt = `NOT EXISTS (
	SELECT 1 FROM built_image_slots slot
	WHERE slot.installation_id=built_image_attempts.installation_id
	AND slot.storage_scope=built_image_attempts.id
	AND slot.policy_generation=built_image_attempts.policy_generation
	AND slot.storage_path=built_image_attempts.storage_path)`

// reconcileBuiltImagePurge waits for admitted transfers to leave storage
// before confirming any generation is empty. Longer-lived leases still pin
// their closure, and retired roots are opened without applying new limits.
func (c *imageCacheRuntime) reconcileBuiltImagePurge(ctx context.Context) error {
	if !c.mu.TryLock() {
		return nil
	}
	defer c.mu.Unlock()
	config, err := c.configuration(ctx)
	if err != nil {
		return err
	}
	policy, err := imagecache.ReadBuiltPolicy(ctx, c.db)
	if err != nil {
		return err
	}
	effectivePath := config.Effective.StoragePath
	roots := []string{effectivePath}
	for _, location := range config.RetiredLocations {
		if location.StoragePath != effectivePath {
			roots = append(roots, location.StoragePath)
		}
	}
	now := builtImageTime(c.now())
	cutoff := policy.PurgeCutoff.Int64
	allComplete := policy.PurgeCutoff.Valid
	var reconcileErr error
	for _, path := range roots {
		complete, err := c.reclaimBuiltImageRoot(ctx, config.Effective.Settings, path, now, cutoff)
		allComplete = allComplete && complete && err == nil
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reclaim built image root %q: %w", path, err))
		}
	}
	if !allComplete {
		return reconcileErr
	}
	return c.finishBuiltImagePurge(ctx, cutoff)
}

func (c *imageCacheRuntime) reclaimBuiltImageRoot(ctx context.Context, effective imagecache.Settings, path, now string, cutoff int64) (bool, error) {
	expired, err := c.expiredBuiltScopes(ctx, path, now, cutoff)
	if err != nil {
		return false, err
	}
	if cutoff == 0 && len(expired) == 0 {
		return true, nil
	}
	var complete bool
	var reclaimed []imagecache.BuiltScope
	if path == effective.StoragePath && c.active != c.stream {
		if err := imagecache.CheckStoragePath(c.installationDir, c.volumeID, effective); err != nil {
			return false, err
		}
		complete, reclaimed, err = c.active.ReclaimBuilt(ctx, cutoff, expired)
	} else {
		complete, reclaimed, err = imagecache.ReclaimManagedBuilt(ctx, c.installationDir, c.volumeID, path, cutoff, expired)
	}
	if err != nil {
		return false, err
	}
	if len(reclaimed) == 0 {
		return complete, nil
	}
	return complete, database.Transaction(ctx, c.db, func(tx *sql.Tx) error {
		for _, scope := range reclaimed {
			if _, err := tx.ExecContext(ctx, `DELETE FROM built_image_attempts
				WHERE id=$1 AND policy_generation=$2 AND storage_path=$3 AND expires_at<=$4
				AND `+unreferencedBuiltAttempt, scope.ID, scope.Generation, path, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *imageCacheRuntime) expiredBuiltScopes(ctx context.Context, path, now string, cutoff int64) ([]imagecache.BuiltScope, error) {
	var scopes []imagecache.BuiltScope
	err := database.Transaction(ctx, c.db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,policy_generation FROM built_image_attempts
			WHERE installation_id=(SELECT id FROM installations WHERE singleton=1 AND state='active')
			AND storage_path=$1 AND expires_at<=$2 AND policy_generation>$3
			AND `+unreferencedBuiltAttempt+` ORDER BY expires_at,id LIMIT 128`, path, now, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var scope imagecache.BuiltScope
			if err := rows.Scan(&scope.ID, &scope.Generation); err != nil {
				return err
			}
			scopes = append(scopes, scope)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, scope := range scopes {
			if _, err := tx.ExecContext(ctx, `UPDATE built_image_attempts SET state='expired'
				WHERE id=$1 AND state IN ('admitted','receiving','complete')`, scope.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return scopes, err
}

func (c *imageCacheRuntime) finishBuiltImagePurge(ctx context.Context, cutoff int64) error {
	return database.Transaction(ctx, c.db, func(tx *sql.Tx) error {
		locked, err := tx.ExecContext(ctx, `UPDATE installations SET version=version
			WHERE singleton=1 AND state='active' AND built_image_purge_cutoff=$1`, cutoff)
		if err != nil {
			return err
		}
		count, err := locked.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		policy, err := imagecache.ReadBuiltPolicy(ctx, tx)
		if err != nil {
			return err
		}
		// An intervening Off save preserves the oldest pending cutoff. Once
		// it is cleaned, request another pass through the newest disabled
		// generation even if that generation was already re-enabled.
		latestDisabled := policy.Generation - 1
		if policy.Enabled {
			latestDisabled--
		}
		var next sql.NullInt64
		if latestDisabled > cutoff {
			next = sql.NullInt64{Int64: latestDisabled, Valid: true}
		}
		for _, query := range []string{
			`DELETE FROM built_image_slots WHERE installation_id=(SELECT id FROM installations WHERE singleton=1)
				AND policy_generation<=$1`,
			`DELETE FROM built_image_attempts WHERE installation_id=(SELECT id FROM installations WHERE singleton=1)
				AND policy_generation<=$1`,
		} {
			if _, err := tx.ExecContext(ctx, query, cutoff); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE installations SET built_image_purge_cutoff=$1 WHERE singleton=1", next)
		return err
	})
}
