package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/identity"
)

var errPostgresDexStorage = errors.New("retained PostgreSQL identity configuration is missing, invalid, or conflicts with setup")

// PostgresSetupCredentials holds the scoped setup logins and optional CA file.
type PostgresSetupCredentials struct {
	ApplicationURI, DexURI, CAFile string
}

// retainPostgresDexStorage stages the scoped Dex login in the target
// installation transaction after verifying the PostgreSQL roles during setup.
// Retrying the same settings preserves the original ciphertext.
func retainPostgresDexStorage(ctx context.Context, tx *sql.Tx, installation string, credentials PostgresSetupCredentials, cipher *cryptostore.Store) error {
	if cipher == nil || installation == "" {
		return errPostgresDexStorage
	}
	target, err := postgresDexStorage(credentials.ApplicationURI, credentials.DexURI, credentials.CAFile)
	if err != nil {
		return err
	}
	var profile string
	if err := tx.QueryRowContext(ctx, "SELECT data_profile FROM installations WHERE id=$1 AND state='configuring'", installation).
		Scan(&profile); err != nil || profile != "postgres" {
		return errPostgresDexStorage
	}
	binding := dexStorageContext(installation)
	existing, err := readDexStorage(ctx, tx, binding, cipher)
	if err == nil {
		if existing != target {
			return errPostgresDexStorage
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return errPostgresDexStorage
	}
	plaintext, err := json.Marshal(target)
	if err != nil {
		return errPostgresDexStorage
	}
	defer clear(plaintext)
	sealed, err := cipher.Seal(binding, plaintext)
	if err != nil {
		return errPostgresDexStorage
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return errPostgresDexStorage
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO protected_values(id,value_json) VALUES($1,$2)", binding.RecordID, string(encoded)); err != nil {
		return errPostgresDexStorage
	}
	return nil
}

func loadPostgresDexStorage(ctx context.Context, db *sql.DB, installation, applicationURI string, cipher *cryptostore.Store) (*identity.DexPostgres, error) {
	if cipher == nil || installation == "" {
		return nil, errPostgresDexStorage
	}
	target, err := readDexStorage(ctx, db, dexStorageContext(installation), cipher)
	if err != nil || !matchesApplication(applicationURI, target) {
		return nil, errPostgresDexStorage
	}
	return &target, nil
}

func readDexStorage(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, binding cryptostore.Context, cipher *cryptostore.Store) (identity.DexPostgres, error) {
	var target identity.DexPostgres
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", binding.RecordID).Scan(&raw); err != nil {
		return target, err
	}
	var value cryptostore.Value
	if json.Unmarshal([]byte(raw), &value) != nil {
		return target, errPostgresDexStorage
	}
	plaintext, err := cipher.Open(binding, value)
	if err != nil {
		return target, errPostgresDexStorage
	}
	defer clear(plaintext)
	if json.Unmarshal(plaintext, &target) != nil || target.Password == "" ||
		(target.CAFile != "system" && !filepath.IsAbs(target.CAFile)) {
		return identity.DexPostgres{}, errPostgresDexStorage
	}
	return target, nil
}

func postgresDexStorage(applicationURI, dexURI, caPath string) (identity.DexPostgres, error) {
	dex, err := parseDexStorageURI(dexURI)
	if err != nil {
		return identity.DexPostgres{}, err
	}
	query := dex.Query()
	password, _ := dex.User.Password()
	if password == "" || len(query) != 1 || (caPath != "" && !filepath.IsAbs(caPath)) {
		return identity.DexPostgres{}, errPostgresDexStorage
	}
	if caPath == "" {
		caPath = "system"
	}
	target := identity.DexPostgres{Host: dex.Hostname(), Port: dexPort(dex), Database: dex.Path[1:],
		User: dex.User.Username(), Password: password, CAFile: caPath}
	if !matchesApplication(applicationURI, target) {
		return identity.DexPostgres{}, errPostgresDexStorage
	}
	return target, nil
}

func matchesApplication(applicationURI string, target identity.DexPostgres) bool {
	app, err := parseDexStorageURI(applicationURI)
	if err != nil {
		return false
	}
	// pgx allows query parameters to override URI components. Accept only
	// connection timing and CA selection alongside the TLS verification mode.
	for key := range app.Query() {
		if key != "sslmode" && key != "sslrootcert" && key != "connect_timeout" {
			return false
		}
	}
	return strings.EqualFold(app.Hostname(), target.Host) && dexPort(app) == target.Port &&
		app.Path[1:] == target.Database && target.User != "" && app.User.Username() != target.User
}

func parseDexStorageURI(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil ||
		u.User.Username() == "" || u.Hostname() == "" || strings.ContainsAny(u.Hostname(), ",/") ||
		len(u.Path) < 2 || strings.Contains(u.Path[1:], "/") || u.Fragment != "" || dexPort(u) == 0 {
		return nil, errPostgresDexStorage
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["sslmode"]) != 1 || query.Get("sslmode") != "verify-full" {
		return nil, errPostgresDexStorage
	}
	return u, nil
}

func dexPort(uri *url.URL) uint16 {
	if uri.Port() == "" {
		return 5432
	}
	port, _ := strconv.ParseUint(uri.Port(), 10, 16)
	return uint16(port)
}

func dexStorageContext(installation string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "dex_postgres", Scope: "installation",
		OwnerID: installation, RecordID: installation + ":dex-postgres", Name: "scoped_credentials"}
}
