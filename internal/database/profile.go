package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrBootstrapUnavailable indicates that the local database cannot be used for setup.
var ErrBootstrapUnavailable = errors.New("installation is unavailable for local bootstrap")

// BootstrapSeed contains the initial installation values supplied by controller startup.
type BootstrapSeed struct {
	ID              string
	Organization    string
	Origin          string
	IDEPattern      string
	PreviewPattern  string
	Release         string
	MinGuestRelease string
	Now             time.Time
}

// OpenBootstrap opens a dedicated SQLite setup database and returns its durable
// installation ID. Initial values are used only when no installation exists.
func OpenBootstrap(ctx context.Context, path string, seed BootstrapSeed) (*sql.DB, string, error) {
	db, err := Open(ctx, Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		return nil, "", err
	}
	if err := Migrate(ctx, db, "sqlite"); err != nil {
		db.Close()
		return nil, "", err
	}
	var installationID string
	err = Transaction(ctx, db, func(tx *sql.Tx) error {
		var state, profile string
		err := tx.QueryRowContext(ctx, `SELECT id, state, data_profile FROM installations WHERE singleton=1`).Scan(&installationID, &state, &profile)
		if err == nil {
			if profile != "sqlite" || (state != "bootstrap" && state != "configuring") {
				return ErrBootstrapUnavailable
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if seed.Now.IsZero() || strings.TrimSpace(seed.Release) == "" || strings.TrimSpace(seed.MinGuestRelease) == "" ||
			seed.Origin == "" || seed.IDEPattern == "" || seed.PreviewPattern == "" {
			return errors.New("initial installation settings are incomplete")
		}
		stamp := seed.Now.UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `INSERT INTO installations
			(id, state, organization_name, data_profile, management_origin, ide_host_pattern, preview_host_pattern,
			 active_release, min_guest_release, created_at, updated_at, version, session_timeout_value, session_timeout_unit)
			VALUES ($1, 'bootstrap', $2, 'sqlite', $3, $4, $5, $6, $7, $8, $8, 1, 8, 'hours')`,
			seed.ID, seed.Organization, seed.Origin, seed.IDEPattern, seed.PreviewPattern, seed.Release, seed.MinGuestRelease, stamp)
		installationID = seed.ID
		return err
	})
	if err != nil {
		db.Close()
		return nil, "", err
	}
	return db, installationID, nil
}
