package database

import (
	"context"
	"database/sql"
	"fmt"
)

// Transaction commits only a successful management mutation. External effects
// belong after this returns, never inside the callback.
func Transaction(ctx context.Context, db *sql.DB, change func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin management transaction: %w", err)
	}
	defer tx.Rollback() // A committed transaction is already closed.
	if err := change(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit management transaction: %w", err)
	}
	return nil
}
