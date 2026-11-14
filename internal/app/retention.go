package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/config"
)

// RunHistoryRetention prunes management history on startup and daily afterward.
// Cleanup failures are logged without interrupting management or reconciliation.
func RunHistoryRetention(ctx context.Context, db *sql.DB, cfg config.Config) error {
	return runHistoryRetention(ctx, 24*time.Hour, func(ctx context.Context) error {
		now := time.Now().UTC()
		_, auditErr := audit.Prune(ctx, db, now.AddDate(0, 0, -cfg.AuditHistoryDays()))
		_, noticeErr := PruneDismissedNotices(ctx, db, now, now.AddDate(0, 0, -cfg.DismissedNoticeHistoryDays()))
		return errors.Join(auditErr, noticeErr)
	}, func(error) {
		slog.WarnContext(ctx, "Management history cleanup failed; check database availability and schema")
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
