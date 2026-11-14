package backup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StagePostgresCheckpoint exports the two owned schemas from a common snapshot.
// The write fence covers snapshot and protected-file acquisition. The dedicated
// snapshot connection stays open during the native dumps after writes resume.
func StagePostgresCheckpoint(ctx context.Context, dir, applicationURI, dexURI string, inputs ProtectedInputs, pause func(context.Context) (func(), error)) (map[string]string, error) {
	if !privateCheckpointDirectory(dir) || pause == nil {
		return nil, errors.New("PostgreSQL backup requires private staging and a write fence")
	}
	app, err := parseDumpTarget(applicationURI, "shiftbox", "application.dump")
	if err != nil {
		return nil, err
	}
	dex, err := parseDumpTarget(dexURI, "dex", "dex.dump")
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(app.uri.Hostname(), dex.uri.Hostname()) || app.uri.Port() != dex.uri.Port() ||
		app.uri.Path != dex.uri.Path || app.uri.User.Username() == dex.uri.User.Username() {
		return nil, errors.New("PostgreSQL backup requires separate owners on the same database")
	}
	application, err := connectDumpOwner(ctx, app)
	if err != nil {
		return nil, err
	}
	defer application.Close(context.Background())
	identity, err := connectDumpOwner(ctx, dex)
	if err != nil {
		return nil, err
	}
	defer identity.Close(context.Background())
	binary, err := exec.LookPath("pg_dump")
	if err != nil {
		return nil, errors.New("PostgreSQL backup requires pg_dump")
	}
	if err := checkPostgresToolVersion(ctx, binary, "pg_dump", application); err != nil {
		return nil, err
	}

	sources := make(map[string]string, 5)
	var tx pgx.Tx
	defer func() {
		if tx != nil {
			tx.Rollback(context.Background())
		}
	}()
	var snapshot string
	if err := captureCheckpoint(ctx, pause, func(ctx context.Context) error {
		wait, cancel := context.WithTimeout(ctx, 5*time.Second)
		release, err := lockPostgresDex(wait, identity)
		cancel()
		if err != nil {
			return err
		}
		defer release()
		tx, err = application.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return errors.New("cannot start shared PostgreSQL checkpoint")
		}
		if err := tx.QueryRow(ctx, "SELECT pg_catalog.pg_export_snapshot()").Scan(&snapshot); err != nil {
			return errors.New("cannot acquire shared PostgreSQL checkpoint")
		}
		return stageProtectedInputs(ctx, dir, inputs, sources)
	}); err != nil {
		return nil, err
	}
	for _, target := range []dumpTarget{app, dex} {
		path := filepath.Join(dir, target.filename)
		if err := dumpPostgresSchema(ctx, binary, path, snapshot, target); err != nil {
			return nil, err
		}
		sources[target.filename] = path
	}
	return sources, nil
}

// Dex's own key rotation and cleanup bypass the controller HTTP fence. Wait
// for their writes to settle and hold a shared lock until snapshot export.
func lockPostgresDex(ctx context.Context, conn *pgx.Conn) (func(), error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, errors.New("cannot start PostgreSQL identity checkpoint")
	}
	release := func() { _ = tx.Rollback(context.Background()) }
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='dex' AND c.relkind IN ('r','p') ORDER BY c.relname`)
	if err != nil {
		release()
		return nil, errors.New("cannot read PostgreSQL identity tables")
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tables) == 0 {
		release()
		return nil, errors.New("PostgreSQL identity store is not initialized")
	}
	for i, table := range tables {
		tables[i] = pgx.Identifier{"dex", table}.Sanitize()
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE "+strings.Join(tables, ",")+" IN SHARE MODE"); err != nil {
		release()
		return nil, errors.New("cannot drain PostgreSQL identity writes for checkpoint")
	}
	return release, nil
}

type dumpTarget struct {
	uri              *url.URL
	password         string
	schema, filename string
}

func parseDumpTarget(raw, schema, filename string) (dumpTarget, error) {
	invalid := errors.New("PostgreSQL backup requires an explicit scoped connection with verified TLS")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil ||
		u.User.Username() == "" || u.Hostname() == "" || strings.ContainsAny(u.Hostname(), ",/") ||
		len(u.Path) < 2 || strings.Contains(u.Path[1:], "/") || u.Fragment != "" {
		return dumpTarget{}, invalid
	}
	password, _ := u.User.Password()
	if password == "" || strings.ContainsAny(password, "\r\n\x00") {
		return dumpTarget{}, invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || q.Get("sslmode") != "verify-full" {
		return dumpTarget{}, invalid
	}
	for key, value := range q {
		if len(value) != 1 || !slices.Contains([]string{"sslmode", "sslrootcert", "connect_timeout"}, key) {
			return dumpTarget{}, invalid
		}
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return dumpTarget{}, invalid
	}
	if q.Get("sslrootcert") == "" {
		q.Set("sslrootcert", "system")
	}
	if q.Get("connect_timeout") == "" {
		q.Set("connect_timeout", "5")
	}
	u.RawQuery = q.Encode()
	u.Host = net.JoinHostPort(u.Hostname(), port)
	u.User = url.User(u.User.Username())
	return dumpTarget{uri: u, password: password, schema: schema, filename: filename}, nil
}

func connectDumpOwner(ctx context.Context, target dumpTarget) (*pgx.Conn, error) {
	u := *target.uri
	u.User = url.UserPassword(u.User.Username(), target.password)
	q := u.Query()
	q.Set("passfile", "")
	u.RawQuery = q.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return nil, errors.New("cannot open scoped PostgreSQL backup connection")
	}
	var owned bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid=n.nspowner
		WHERE n.nspname=$1 AND r.rolname=current_user AND NOT r.rolsuper
	)`, target.schema).Scan(&owned)
	if err != nil || !owned {
		conn.Close(context.Background())
		return nil, errors.New("PostgreSQL backup connection must own its scoped schema")
	}
	return conn, nil
}

func checkPostgresToolVersion(ctx context.Context, binary, tool string, conn *pgx.Conn) error {
	var major int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&major); err != nil {
		return errors.New("cannot determine PostgreSQL backup server version")
	}
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), fmt.Sprintf("%s (PostgreSQL) %d.", tool, major)) {
		return fmt.Errorf("%s must match the PostgreSQL server major version", tool)
	}
	return nil
}

func dumpPostgresSchema(ctx context.Context, binary, path, snapshot string, target dumpTarget) error {

	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot stage PostgreSQL archive")
	}
	command := exec.CommandContext(ctx, binary, "--dbname="+target.uri.String(), "--no-password",
		"--format=custom", "--schema="+target.schema, "--strict-names", "--no-large-objects", "--snapshot="+snapshot)

	command.Stdout = output
	if err := errors.Join(runPostgresTool(command, filepath.Dir(path), target.password), output.Close()); err != nil {
		os.Remove(path)
		return errors.New("cannot export scoped PostgreSQL schema")
	}
	return nil
}

// Use a short-lived private password file rather than command arguments or
// environment variables that can be inspected by other local processes.
func runPostgresTool(command *exec.Cmd, directory, password string) error {
	passfile, err := os.CreateTemp(directory, ".pgpass-")
	if err != nil {
		return errors.New("cannot stage protected PostgreSQL credentials")
	}
	defer os.Remove(passfile.Name())
	escape := strings.NewReplacer("\\", "\\\\", ":", "\\:")
	_, err = fmt.Fprintf(passfile, "*:*:*:*:%s\n", escape.Replace(password))
	if err := errors.Join(err, passfile.Close()); err != nil {
		return errors.New("cannot stage protected PostgreSQL credentials")
	}

	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PG") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "PGPASSFILE="+passfile.Name())
	return command.Run()
}
