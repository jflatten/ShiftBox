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

// Rollback refuses an in-place downgrade; recovery is a separately reviewed workflow.
func Rollback(context.Context, *sql.DB, string) error { return ErrRecoveryRequired }
