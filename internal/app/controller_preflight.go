package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/secret"
)

// ControllerState carries the active database and trust material loaded at startup.
type ControllerState struct {
	DB                 *sql.DB
	Installation       ActiveInstallation
	BrowserTLS         tls.Certificate
	GuestCA            *guest.Authority
	DexPostgres        *identity.DexPostgres
	Secrets            *secret.Service
	cipher             *cryptostore.Store
	browserRoots       *x509.CertPool
	browserReplacement atomic.Pointer[servedBrowserCertificate]
	retentionMaximum   *int64
	retentionGrace     time.Duration
	retentionStatus    atomic.Pointer[retentionSnapshot]
}

// Close releases the authoritative database on shutdown or failed composition.
func (s *ControllerState) Close() error { return s.DB.Close() }

// GitHub binds a configured App to the active installation's protected store.
func (s *ControllerState) GitHub(clientID, clientSecret string, installationID int64) (*identity.GitHub, error) {
	return identity.NewGitHub(s.DB, s.cipher, clientID, clientSecret, installationID, time.Now)
}

// PreflightController loads the database and retained TLS material before any
// listener can be opened. It uses the system roots for browser certificates.
func PreflightController(ctx context.Context, cfg config.Config) (*ControllerState, error) {
	return preflightController(ctx, cfg, OpenActiveDatabase)
}

// PreflightServingController admits recovery state only with a durable review.
func PreflightServingController(ctx context.Context, cfg config.Config) (*ControllerState, error) {
	return preflightController(ctx, cfg, openServingDatabase)
}

func preflightController(ctx context.Context, cfg config.Config,
	open func(context.Context, config.Config) (*sql.DB, ActiveInstallation, error)) (*ControllerState, error) {
	db, active, err := open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	state, err := loadControllerState(ctx, db, active, cfg)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return state, nil
}

func loadControllerState(ctx context.Context, db *sql.DB, active ActiveInstallation, cfg config.Config) (*ControllerState, error) {
	dir := cfg.InstallationDir
	cipher, err := loadControllerCipher(dir)
	if err != nil {
		conditionErr := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return writeOperationalCondition(ctx, tx, keyUnavailable, true, time.Now())
		})
		return nil, errors.Join(err, conditionErr)
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
	state := &ControllerState{DB: db, Installation: active, BrowserTLS: browser, GuestCA: authority,
		Secrets: secret.NewService(cipher), cipher: cipher, browserRoots: roots,
		retentionMaximum: cfg.WorkspaceRetentionMaximumSeconds, retentionGrace: cfg.RetentionOutageGrace()}
	if active.Settings.DataProfile == "postgres" {
		state.DexPostgres, err = loadPostgresDexStorage(ctx, db, active.InstallationID, cfg.PostgresURI, cipher)
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}

func loadControllerCipher(dir string) (*cryptostore.Store, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("controller requires a private installation directory")
	}
	key, err := cryptostore.LoadKey(filepath.Join(dir, "installation.key"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return cryptostore.New(key)
}
