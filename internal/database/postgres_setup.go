package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	applicationSchema = "shiftbox"
	dexSchema         = "dex"
)

// PostgresSetupTarget contains the scoped service logins and operator trust
// for a pre-provisioned PostgreSQL target. URIs are never returned in diagnostics.
type PostgresSetupTarget struct {
	ApplicationURI string
	DexURI         string
	Roots          *x509.CertPool
}

// PreflightPostgresSetup verifies an empty, separately owned target without
// issuing DDL or moving installation authority. Each role connects with its
// database-side search path, just as Dex does on startup.
func PreflightPostgresSetup(ctx context.Context, target PostgresSetupTarget) error {
	if target.Roots == nil {
		return errors.New("PostgreSQL setup requires server certificate trust")
	}
	app, err := setupPostgresConfig(target.ApplicationURI, target.Roots)
	if err != nil {
		return err
	}
	dex, err := setupPostgresConfig(target.DexURI, target.Roots)
	if err != nil {
		return err
	}
	if !strings.EqualFold(app.Host, dex.Host) || app.Port != dex.Port || app.Database != dex.Database || app.User == dex.User {
		return errors.New("setup requires separate ShiftBox and Dex roles on the same PostgreSQL database")
	}
	if err := checkSetupNamespace(ctx, app, applicationSchema, dexSchema); err != nil {
		return err
	}
	return checkSetupNamespace(ctx, dex, dexSchema, applicationSchema)
}

func setupPostgresConfig(raw string, roots *x509.CertPool) (*pgx.ConnConfig, error) {
	invalid := errors.New("provide an explicit PostgreSQL URI with credentials, a database, and sslmode=verify-full")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil ||
		u.User.Username() == "" || u.Hostname() == "" || strings.ContainsAny(u.Hostname(), ",/") ||
		len(u.Path) < 2 || strings.Contains(u.Path[1:], "/") || u.Fragment != "" {
		return nil, invalid
	}
	if password, ok := u.User.Password(); !ok || password == "" {
		return nil, invalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "verify-full" {
		return nil, invalid
	}
	// Configure TLS directly so an uploaded URI cannot select controller files
	// or override schema placement. Parsing here performs no network connection.
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "5432")
	}
	u.RawQuery = "sslmode=disable&passfile=&connect_timeout=5"
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, invalid
	}
	cfg.TLSConfig = &tls.Config{ServerName: u.Hostname(), RootCAs: roots, MinVersion: tls.VersionTLS12}
	cfg.Fallbacks = nil
	cfg.SSLNegotiation = "postgres"
	cfg.RuntimeParams = nil
	return cfg, nil
}

func checkSetupNamespace(ctx context.Context, cfg *pgx.ConnConfig, schema, sibling string) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("cannot connect to PostgreSQL with verified TLS and the scoped setup credentials")
	}
	defer conn.Close(context.Background())
	if err := checkSetupRole(ctx, conn, cfg.User, schema, sibling); err != nil {
		return err
	}

	var empty bool
	err = conn.QueryRow(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM pg_depend d JOIN pg_namespace n ON d.refobjid=n.oid
		WHERE d.refclassid='pg_namespace'::regclass AND n.nspname=$1
	)`, schema).Scan(&empty)
	if err != nil || !empty {
		return errors.New("PostgreSQL setup requires empty ShiftBox and Dex schemas")
	}
	return nil
}

// OpenPostgresSetupApplication opens the scoped application database after preflight.
// It checks schema isolation on every fresh connection, including after migrations.
func OpenPostgresSetupApplication(ctx context.Context, target PostgresSetupTarget) (*sql.DB, error) {
	if target.Roots == nil {
		return nil, errors.New("PostgreSQL setup requires server certificate trust")
	}
	cfg, err := setupPostgresConfig(target.ApplicationURI, target.Roots)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		return checkSetupRole(ctx, conn, cfg.User, applicationSchema, dexSchema)
	}))
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("cannot open the scoped PostgreSQL application schema")
	}
	return db, nil
}

func checkSetupRole(ctx context.Context, conn *pgx.Conn, user, schema, sibling string) error {
	return scanPostgresRole(conn.QueryRow(ctx, postgresRoleCheck, user, schema, sibling))
}

// CheckPostgresMigrationRoles checks that the active service roles still own
// separate schemas. A fresh Dex connection uses the server's search path.
func CheckPostgresMigrationRoles(ctx context.Context, app *sql.DB, dexURI string, roots *x509.CertPool) error {
	if err := scanPostgresRole(app.QueryRowContext(ctx, postgresRoleCheck, "", applicationSchema, dexSchema)); err != nil {
		return err
	}
	cfg, err := setupPostgresConfig(dexURI, roots)
	if err != nil {
		return err
	}
	dex, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.New("cannot verify the scoped PostgreSQL Dex login")
	}
	defer dex.Close(context.Background())
	return checkSetupRole(ctx, dex, cfg.User, dexSchema, applicationSchema)
}

const postgresRoleCheck = `SELECT
	($1 = '' OR current_user = $1)
	AND current_schemas(false) = ARRAY[$2]::name[]
	AND NOT (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
	AND NOT has_database_privilege(current_database(), 'CREATE')
	AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname=$2 AND n.nspowner=r.oid)
	AND EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname=$3 AND
		n.nspowner<>r.oid AND NOT has_schema_privilege(n.oid, 'USAGE,CREATE'))
	AND NOT EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)
	AND NOT EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname<>$2
		AND has_schema_privilege(n.oid, 'CREATE'))
	FROM pg_roles r WHERE r.rolname=current_user`

func scanPostgresRole(row interface{ Scan(...any) error }) error {
	var isolated bool
	if err := row.Scan(&isolated); err != nil || !isolated {
		return errors.New("PostgreSQL service role must own only its isolated schema and use it as its search path")
	}
	return nil
}
