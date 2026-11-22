package app

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/release"
)

// MigrationEvidence checks retained guest observations and the active
// installation materials loaded by controller preflight.
func (s *ControllerState) MigrationEvidence(ctx context.Context, evidence release.MigrationEvidence, installationDir, protocol string, now time.Time) (release.MigrationEvidence, error) {
	guests, err := release.NewStore(s.DB).GuestUpgradeBlockers(ctx, s.Installation.InstallationID, s.Installation.ActiveRelease, protocol)
	if err != nil {
		return release.MigrationEvidence{}, err
	}
	evidence.Guests = guests
	metadata, err := s.BackupMetadata(ctx)
	if err != nil {
		return release.MigrationEvidence{}, err
	}
	evidence.VerifiedBackup, err = backup.HasCompletedBackup(ctx, s.DB, metadata)
	if err != nil {
		return release.MigrationEvidence{}, err
	}
	evidence.DatabaseOwned = s.migrationDatabaseOwned(ctx)
	evidence.KeyAccessible = s.migrationKeyAccessible(installationDir)
	browser, err := s.browserCertificateAt(now)
	evidence.BrowserValid = err == nil && browser.Leaf != nil
	if s.GuestCA != nil && s.GuestCA.Certificate != nil {
		evidence.GuestCAUntil = s.GuestCA.Certificate.NotAfter
	}
	return evidence, nil
}

func (s *ControllerState) migrationKeyAccessible(dir string) bool {
	retained, err := loadControllerCipher(dir)
	if err != nil || s.cipher == nil {
		return false
	}
	// An in-memory check proves the retained key can open data encrypted by
	// the running controller without reading any application secret.
	binding := cryptostore.Context{KeyVersion: 1, RecordType: "migration_preflight", Scope: "installation"}
	probe, err := s.cipher.Seal(binding, nil)
	if err != nil {
		return false
	}
	_, err = retained.Open(binding, probe)
	return err == nil
}

func (s *ControllerState) migrationDatabaseOwned(ctx context.Context) bool {
	if s.Installation.Settings.DataProfile == "sqlite" {
		return true // The active SQLite schema was checked when opening the installation.
	}
	dex := s.DexPostgres
	if s.Installation.Settings.DataProfile != "postgres" || dex == nil {
		return false
	}
	roots, err := postgresTrustRoots(dex.CAFile)
	if err != nil {
		return false
	}
	uri := url.URL{Scheme: "postgres", Host: net.JoinHostPort(dex.Host, strconv.Itoa(int(dex.Port))),
		Path: "/" + dex.Database, User: url.UserPassword(dex.User, dex.Password), RawQuery: "sslmode=verify-full"}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return database.CheckPostgresMigrationRoles(ctx, s.DB, uri.String(), roots) == nil
}
