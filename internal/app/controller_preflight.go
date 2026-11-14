package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/guest"
)

// ControllerState carries the active database and trust material loaded at startup.
type ControllerState struct {
	DB           *sql.DB
	Installation ActiveInstallation
	BrowserTLS   tls.Certificate
	GuestCA      *guest.Authority
}

// Close releases the authoritative database on shutdown or failed composition.
func (s *ControllerState) Close() error { return s.DB.Close() }

// PreflightController loads the database and retained TLS material before any
// listener can be opened. It uses the system roots for browser certificates.
func PreflightController(ctx context.Context, cfg config.Config) (*ControllerState, error) {
	db, active, err := OpenActiveDatabase(ctx, cfg)
	if err != nil {
		return nil, err
	}
	state, err := loadControllerState(ctx, db, active, cfg.InstallationDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return state, nil
}

func loadControllerState(ctx context.Context, db *sql.DB, active ActiveInstallation, dir string) (*ControllerState, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("controller startup requires a private installation directory")
	}
	key, err := cryptostore.LoadKey(filepath.Join(dir, "installation.key"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	cipher, err := cryptostore.New(key)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	authority, err := LoadInstallationCA(dir, active.InstallationID, cipher, now)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load system certificate trust")
	}
	browser, err := config.LoadBrowserCertificate(ctx, db, active.InstallationID,
		active.Settings.BrowserCertificateID, cipher, roots, now)
	if err != nil {
		return nil, errors.New("cannot validate the active browser certificate")
	}
	return &ControllerState{DB: db, Installation: active, BrowserTLS: browser, GuestCA: authority}, nil
}
