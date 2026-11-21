package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/launchlog"
)

// RunHistoryRetention prunes management history on startup and daily afterward.
// Cleanup failures are logged without interrupting management or reconciliation.
func RunHistoryRetention(ctx context.Context, db *sql.DB, cfg config.Config, fence *backup.WriteFence) error {
	return runHistoryRetention(ctx, 24*time.Hour, func(ctx context.Context) error {
		return withCheckpointActivity(ctx, fence, func() error {
			now := time.Now().UTC()
			_, auditErr := audit.Prune(ctx, db, now.AddDate(0, 0, -cfg.AuditHistoryDays()))
			_, noticeErr := PruneDismissedNotices(ctx, db, now, now.AddDate(0, 0, -cfg.DismissedNoticeHistoryDays()))
			return errors.Join(auditErr, noticeErr)
		})
	}, func(error) {
		slog.WarnContext(ctx, "Management history cleanup failed; check database availability and schema")
	})
}

// RunLaunchLogRetention expires completed launch diagnostics at startup and every four hours.
func RunLaunchLogRetention(ctx context.Context, db *sql.DB, fence *backup.WriteFence) error {
	return runHistoryRetention(ctx, 4*time.Hour, func(ctx context.Context) error {
		return withCheckpointActivity(ctx, fence, func() error {
			return launchlog.Prune(ctx, db, time.Now())
		})
	}, func(error) {
		slog.WarnContext(ctx, "Launch log cleanup failed; check database availability")
	})
}

// RunBuiltImageRetention retries disabled-generation and expired upload
// cleanup at startup and periodically while the controller serves workspaces.
func (s *ControllerState) RunBuiltImageRetention(ctx context.Context, fence *backup.WriteFence) error {
	return runHistoryRetention(ctx, time.Minute, func(ctx context.Context) error {
		return withCheckpointActivity(ctx, fence, func() error {
			return s.imageCache.reconcileBuiltImagePurge(ctx)
		})
	}, func(err error) {
		slog.WarnContext(ctx, "Built image cache cleanup will retry", "error", err)
	})
}

func runHistoryRetention(ctx context.Context, interval time.Duration, prune func(context.Context) error, report func(error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := prune(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

// PruneDismissedNotices drops old dismissal receipts for expired notices
// during active operation. Keeping receipts for current notices prevents them
// from reappearing as unread after cleanup.
func PruneDismissedNotices(ctx context.Context, db *sql.DB, now, cutoff time.Time) (int64, error) {
	const stamp = "2006-01-02T15:04:05.000000000Z"
	result, err := db.ExecContext(ctx, `DELETE FROM notice_receipts
		WHERE dismissed_at < $1
		AND EXISTS (SELECT 1 FROM notices WHERE id=notice_receipts.notice_id AND expires_at <= $2)
		AND EXISTS (SELECT 1 FROM installations WHERE singleton=1 AND state='active')`,
		cutoff.UTC().Format(stamp), now.UTC().Format(stamp))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
