package auth

import (
	"context"
	"database/sql"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// RevokeAndClose commits session invalidation before synchronously closing its
// live connections. Failed closures remain registered and can be retried.
func (s *Store) RevokeAndClose(ctx context.Context, id string, now time.Time, reason string, registry *proxy.Registry) error {
	if err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error { return s.RevokeSession(ctx, tx, id, now, reason) }); err != nil {
		return err
	}
	return registry.CloseMatching(func(scope proxy.Scope) bool { return scope.SessionID == id })
}
