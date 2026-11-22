package audit

import (
	"context"
	"database/sql"
	"time"
)

// Prune removes audit history strictly before cutoff while the installation is
// active. Membership, grants, operations and provenance live in separate tables.
func Prune(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	// Audit rows use UTC RFC3339Nano. Normalize optional fractional seconds
	// before comparing so existing rows keep their exact cutoff ordering.
	const normalized = `substr(occurred_at,1,19) || '.' ||
		substr(replace(substr(occurred_at,21),'Z','') || '000000000',1,9) || 'Z'`
	result, err := db.ExecContext(ctx, `DELETE FROM audit_events WHERE EXISTS (
		SELECT 1 FROM installations WHERE singleton=1 AND state='active')
		AND (`+normalized+`) < $1`, cutoff.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
