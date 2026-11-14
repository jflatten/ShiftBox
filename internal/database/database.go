package database

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
)

// Config selects one explicit data profile. Drivers are registered by composition.
type Config struct {
	Profile     string
	SQLitePath  string
	PostgresURI string
	Schema      string
}

// Open validates ownership before returning a database usable by application stores.
// Connection errors omit driver text because it can contain credentials.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	var driver, dsn string
	switch cfg.Profile {
	case "sqlite":
		if !filepath.IsAbs(cfg.SQLitePath) {
			return nil, errors.New("sqlite path must be absolute")
		}
		u := url.URL{Scheme: "file", Path: cfg.SQLitePath}
		q := url.Values{"_pragma": {"foreign_keys(1)", "busy_timeout(5000)"}}
		u.RawQuery = q.Encode()
		driver, dsn = "sqlite", u.String()
	case "postgres":
		u, err := url.Parse(cfg.PostgresURI)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Query().Get("sslmode") != "verify-full" {
			return nil, errors.New("postgres requires verified TLS")
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(cfg.Schema) || cfg.Schema == "public" || cfg.Schema == "dex" || len(cfg.Schema) > 63 {
			return nil, errors.New("postgres requires a separately owned application schema")
		}
		q := u.Query()
		q.Set("search_path", cfg.Schema)
		u.RawQuery = q.Encode()
		driver, dsn = "pgx", u.String()
	default:
		return nil, errors.New("unsupported database profile")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, errors.New("cannot open configured database")
	}
	// One controller serializes management transactions; this also keeps SQLite
	// connection-local settings and test checkpoints predictable.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("configured database is unavailable")
	}
	if cfg.Profile == "postgres" {
		var owned bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
			WHERE n.nspname = $1 AND r.rolname = current_user AND current_schema() = $1
		)`, cfg.Schema).Scan(&owned)
		if err != nil || !owned {
			db.Close()
			return nil, errors.New("application schema ownership or search path is invalid")
		}
	}
	return db, nil
}
