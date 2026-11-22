package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// RevokeUserSessions invalidates every session of a user in the caller's
// transaction. An earlier revocation keeps its original time and reason.
func (s *Store) RevokeUserSessions(ctx context.Context, tx *sql.Tx, userID string, now time.Time, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason=$2
		WHERE user_id=$3 AND revoked_at IS NULL`, authTime(now), reason, userID)
	return err
}

// RevokeAndClose commits session invalidation before synchronously closing its
// live connections. Failed closures remain registered and can be retried.
func (s *Store) RevokeAndClose(ctx context.Context, id string, now time.Time, reason string, registry *proxy.Registry) error {
	if err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error { return s.RevokeSession(ctx, tx, id, now, reason) }); err != nil {
		return err
	}
	return registry.CloseMatching(func(scope proxy.Scope) bool { return scope.SessionID == id })
}
