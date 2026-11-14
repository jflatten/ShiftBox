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

// ErrSchemaDrift indicates the migration history and foundation tables disagree.
var ErrSchemaDrift = errors.New("installation schema differs from its migration history")

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

// CheckSchema verifies the installed migration history and required foundation
// records. It does not modify the database.
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
	if err := rows.Close(); err != nil {
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
	// Early development builds extended the first migration before release.
	// An already-applied version alone cannot establish that those records exist.
	for _, query := range []string{
		"SELECT id,value_json FROM protected_values WHERE 1=0",
		"SELECT originating_action FROM operations WHERE 1=0",
		"SELECT id,occurred_at,actor_type,actor_id,action,resource_type,resource_id,outcome,reason_code,metadata_json FROM audit_events WHERE 1=0",
		"SELECT id,severity,title,body,effective_at,expires_at,created_by,created_at FROM notices WHERE 1=0",
		"SELECT notice_id,user_id,read_at,dismissed_at FROM notice_receipts WHERE 1=0",
		"SELECT email,user_id,role,credential_json FROM local_user_creations WHERE 1=0",
		"SELECT id,name,mode,rules_json,required_exceptions_json,provider_capability_version,enabled,version FROM network_policies WHERE 1=0",
		"SELECT network_policy_override_json FROM workspaces WHERE 1=0",
		"SELECT installation_id,user_id,operation_id,password_hash,password_ciphertext_id,credential_ready,delivered_at,created_at FROM bootstrap_administrator WHERE 1=0",
	} {
		probe, err := db.QueryContext(ctx, query)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrSchemaDrift
		}
		if err := probe.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Rollback refuses an in-place downgrade; recovery is a separately reviewed workflow.
func Rollback(context.Context, *sql.DB, string) error { return ErrRecoveryRequired }
