package app

import (
	"context"
	"database/sql"
	"time"
)

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
