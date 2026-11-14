package database

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"

	"github.com/jflatten/ShiftBox/migrations"
	"github.com/pressly/goose/v3"
)

// ErrRecoveryRequired prevents treating binary/schema rollback as safe recovery.
var ErrRecoveryRequired = errors.New("schema rollback requires a verified management backup and reviewed recovery")

func migrationProvider(db *sql.DB, profile string) (*goose.Provider, error) {
	var dialect goose.Dialect
	switch profile {
	case "sqlite":
		dialect = goose.DialectSQLite3
	case "postgres":
		dialect = goose.DialectPostgres
	default:
		return nil, errors.New("unsupported migration profile")
	}
	files, err := fs.Sub(migrations.Files, profile)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(dialect, db, files, goose.WithDisableGlobalRegistry(true))
}

// Migrate applies only embedded, ordered migrations to a validated database.
func Migrate(ctx context.Context, db *sql.DB, profile string) error {
	p, err := migrationProvider(db, profile)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// Version reports the installed application schema version for release preflight.
func Version(ctx context.Context, db *sql.DB, profile string) (int64, error) {
	p, err := migrationProvider(db, profile)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// CheckSchema verifies that the running binary and the installed migration
// history agree. Unlike migration commands, it never creates a version table.
func CheckSchema(ctx context.Context, db *sql.DB, profile string) error {
	provider, err := migrationProvider(db, profile)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT version_id,is_applied FROM goose_db_version ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		var present bool
		if err := rows.Scan(&version, &present); err != nil {
			return err
		}
		applied[version] = present
	}
	if err := rows.Err(); err != nil {
		return err
	}
	delete(applied, 0)
	for _, source := range provider.ListSources() {
		if !applied[source.Version] {
			return errors.New("installation schema is missing a required migration")
		}
		delete(applied, source.Version)
	}
	for _, present := range applied {
		if present {
			return errors.New("installation schema contains a migration unknown to this release")
		}
	}
	return nil
}

// Rollback refuses an in-place downgrade; recovery is a separately reviewed workflow.
func Rollback(context.Context, *sql.DB, string) error { return ErrRecoveryRequired }
