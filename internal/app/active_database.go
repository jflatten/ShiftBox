package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// ErrActiveDatabase indicates startup cannot establish the configured database
// as the active installation authority.
var ErrActiveDatabase = errors.New("cannot verify the active installation database; review setup or restore configuration")

// ActiveInstallation is the committed settings snapshot selected for startup.
type ActiveInstallation struct {
	InstallationID      string
	Generation          int64
	Settings            config.InstallationSettings
	ActiveRelease       string
	MinimumGuestRelease string
}

// OpenActiveDatabase opens only the explicitly configured database and checks
// its active authority before returning it. Startup never seeds a database.
func OpenActiveDatabase(ctx context.Context, cfg config.Config) (*sql.DB, ActiveInstallation, error) {
	if cfg.Validate() != nil {
		return nil, ActiveInstallation{}, ErrActiveDatabase
	}
	if cfg.DataProfile == "sqlite" {
		info, err := os.Lstat(cfg.SQLitePath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, ActiveInstallation{}, ErrActiveDatabase
		}
	}
	db, err := database.Open(ctx, database.Config{Profile: cfg.DataProfile, SQLitePath: cfg.SQLitePath,
		PostgresURI: cfg.PostgresURI, Schema: "shiftbox"})
	if err != nil {
		return nil, ActiveInstallation{}, ErrActiveDatabase
	}
	active, err := readActiveInstallation(ctx, db, cfg)
	if err != nil {
		_ = db.Close()
		return nil, ActiveInstallation{}, err
	}
	if err := database.CheckSchema(ctx, db, cfg.DataProfile); err != nil {
		_ = db.Close()
		if errors.Is(err, database.ErrSchemaDrift) {
			return nil, ActiveInstallation{}, fmt.Errorf("%w: %w", ErrActiveDatabase, err)
		}
		return nil, ActiveInstallation{}, ErrActiveDatabase
	}
	return db, active, nil
}

func readActiveInstallation(ctx context.Context, db *sql.DB, cfg config.Config) (ActiveInstallation, error) {
	var active ActiveInstallation
	var stored config.InstallationSettings
	var snapshot string
	err := db.QueryRowContext(ctx, `SELECT i.id,i.config_generation,i.organization_name,i.data_profile,
		i.management_origin,i.ide_host_pattern,i.preview_host_pattern,i.external_auth_interval_seconds,
		i.active_release,i.min_guest_release,a.settings_json
		FROM installations i JOIN activated_configurations a
		ON a.installation_id=i.id AND a.generation=i.config_generation AND a.retired_at IS NULL
		WHERE i.singleton=1 AND i.state='active' AND i.config_generation>0`).
		Scan(&active.InstallationID, &active.Generation, &stored.OrganizationName, &stored.DataProfile,
			&stored.ManagementOrigin, &stored.IDEHostPattern, &stored.PreviewHostPattern,
			&stored.ExternalAuthIntervalSeconds, &active.ActiveRelease, &active.MinimumGuestRelease, &snapshot)
	if err != nil || json.Unmarshal([]byte(snapshot), &active.Settings) != nil || active.Settings.BrowserCertificateID == "" {
		return ActiveInstallation{}, ErrActiveDatabase
	}
	stored.BrowserCertificateID = active.Settings.BrowserCertificateID
	if stored != active.Settings || stored.DataProfile != cfg.DataProfile ||
		stored.ManagementOrigin != cfg.ManagementOrigin {
		return ActiveInstallation{}, ErrActiveDatabase
	}
	if _, err := config.NewServiceHosts(stored.ManagementOrigin, stored.IDEHostPattern, stored.PreviewHostPattern); err != nil {
		return ActiveInstallation{}, ErrActiveDatabase
	}
	return active, nil
}
