package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	_ "modernc.org/sqlite"
)

// Set at build time to the release shipped with the pinned guest agent.
var controllerRelease = "development"

func runSetup(ctx context.Context, args []string, out, status io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("installation-dir", "", "")
	email := flags.String("administrator-email", "", "")
	dex := flags.String("dex-binary", "", "")
	postgresPath := flags.String("postgres-identity-config", "", "")
	configPath := flags.String("config", "", "")
	platformFolder := flags.String("platform-folder", "", "")
	reset := flags.Bool("reset-temporary-password", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*dir) {
		return errors.New("setup requires an absolute installation directory and administrator email")
	}
	normalized := strings.ToLower(strings.TrimSpace(*email))
	addr, err := mail.ParseAddress(normalized)
	if err != nil || addr.Address != normalized || addr.Name != "" {
		return errors.New("setup requires a valid administrator email")
	}
	if *dex == "" {
		defaultPath, err := defaultDexPath()
		if err != nil {
			return err
		}
		*dex = defaultPath
	}
	if !filepath.IsAbs(*dex) {
		return errors.New("Dex binary path must be absolute")
	}
	if *reset {
		return resetSetupPassword(ctx, *dir, *dex, normalized, out)
	}
	platform, err := readSetupPlatform(*dir, *configPath, *platformFolder)
	if err != nil {
		return err
	}
	postgres, err := readSetupPostgresProfile(*postgresPath)
	if err != nil {
		return err
	}
	assets, err := browserAssets()
	if err != nil {
		return err
	}
	lock, err := lockSetup(*dir)
	if err != nil {
		return err
	}
	defer lock.Close()

	cipher, err := setupCipher(*dir)
	if err != nil {
		return err
	}
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(*dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	roots, err := x509.SystemCertPool()
	if err != nil {
		return errors.New("cannot load system certificate trust")
	}
	resumed, err := resumeRetiredSetup(ctx, *dir, cipher, roots)
	if err != nil {
		return err
	}
	if resumed {
		_, err := fmt.Fprintln(status, "PostgreSQL installation activated. Start the controller and sign in again.")
		return err
	}
	var setup app.SetupHTTP
	var dexDone <-chan struct{}
	closeDex := func() {}
	defer func() { closeDex() }()
	server, err := app.ListenBootstrap("127.0.0.1:0", cert, func(origin string) (http.Handler, error) {
		db, id, err := database.OpenBootstrap(ctx, filepath.Join(*dir, "bootstrap.db"), database.BootstrapSeed{
			ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin,
			IDEPattern: "{workspace}.ide.invalid", PreviewPattern: "{service}--{workspace}.preview.invalid",
			Release: controllerRelease, MinGuestRelease: controllerRelease, Now: time.Now(),
		})
		if err != nil {
			return nil, err
		}
		if _, err := app.EnsureInstallationCA(*dir, id, cipher, time.Now()); err != nil {
			db.Close()
			return nil, err
		}
		admin, login, done, cleanup, err := startSetupDex(ctx, *dir, *dex, origin, cert, nil)
		if err != nil {
			db.Close()
			return nil, err
		}
		dexDone, closeDex = done, cleanup
		setup = app.SetupHTTP{DB: db, InstallationID: id, Origin: origin, Cipher: cipher, Admin: admin, Login: login, BrowserAssets: assets, CertificateRoots: roots, Postgres: postgres,
			Journal: operation.NewJournal(db, time.Now, uuid.NewString), Now: time.Now, NewID: uuid.NewString}
		if platform != nil {
			setup.Platform = platform.validator(db, id)
		}
		setup = setup.WithPostgresTransfer(func(ctx context.Context, storage identity.DexPostgres, email, userID string) error {
			target, _, _, cleanup, err := startSetupDex(ctx, *dir, *dex, origin, cert, &storage)
			if err != nil {
				return err
			}
			defer cleanup()
			return identity.TransferBootstrapCredential(ctx, filepath.Join(*dir, "dex.db"), email, userID, target)
		})
		handler, err := app.NewSetupHandler(setup)
		if err != nil {
			db.Close()
		}
		return handler, err
	})
	if err != nil {
		return errors.New("cannot open local setup; check installation state and database")
	}
	defer setup.DB.Close()
	defer server.Close()
	if err := deliverAdministrator(ctx, setup, normalized, out); err != nil {
		return err
	}

	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()
	destination := "/auth/login"
	if assets != nil {
		destination = "/setup/"
	}
	if _, err := fmt.Fprintf(status, "Local setup: %s\nVerify the setup certificate SHA-256 fingerprint: %x\nOpen: %s%s\n", server.Origin, sha256.Sum256(cert.Certificate[0]), server.Origin, destination); err != nil {
		stop()
		<-served
		return errors.New("cannot write local setup status")
	}

	select {
	case <-served:
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("local setup listener stopped unexpectedly")
	case <-dexDone:
		wasCanceled := ctx.Err() != nil
		stop()
		<-served
		if wasCanceled {
			return nil
		}
		return errors.New("local identity service stopped")
	}
}

func defaultDexPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", errors.New("cannot locate the managed Dex binary")
	}
	return filepath.Join(filepath.Dir(executable), "dex"), nil
}

func deliverAdministrator(ctx context.Context, setup app.SetupHTTP, email string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	admission, err := app.BeginAdministrator(ctx, setup.DB, setup.Journal, app.AdministratorIntent{
		InstallationID: setup.InstallationID, UserID: uuid.NewString(), Email: email, CreatedAt: time.Now(),
	}, setup.Cipher, "initial-administrator")
	if err != nil {
		return errors.New("cannot prepare the initial administrator; verify the installation and administrator email")
	}
	if _, err := app.FinishAdministrator(ctx, setup.DB, setup.Journal, setup.InstallationID, setup.Admin, admission.Operation); err != nil {
		return errors.New("cannot configure local sign-in; retry setup after checking Dex")
	}
	var delivered sql.NullString
	if err := setup.DB.QueryRowContext(ctx, "SELECT delivered_at FROM bootstrap_administrator WHERE installation_id=$1", setup.InstallationID).Scan(&delivered); err != nil {
		return errors.New("cannot verify temporary password delivery")
	}
	if delivered.Valid {
		return nil
	}
	password, err := app.ClaimTemporaryPassword(ctx, setup.DB, setup.InstallationID, setup.Cipher, time.Now())
	if err != nil {
		return errors.New("cannot claim the temporary password")
	}
	if _, err := fmt.Fprintln(out, password); err != nil {
		return errors.New("temporary password output failed; use protected local recovery")
	}
	return nil
}

func lockSetup(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create installation directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("setup requires a private installation directory")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open setup lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another ShiftBox process holds the installation lock")
	}
	return lock, nil
}

func setupCipher(dir string) (*cryptostore.Store, error) {
	path := filepath.Join(dir, "installation.key")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("installation key must be a private regular file")
		}
		key, err := cryptostore.LoadKey(path)
		if err != nil {
			return nil, err
		}
		return cryptostore.New(key)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect installation key")
	}
	if _, err := os.Lstat(filepath.Join(dir, "bootstrap.db")); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("existing bootstrap database requires its installation key")
	}
	key := make([]byte, 32)
	rand.Read(key)
	file, err := os.CreateTemp(dir, ".installation-key-")
	if err != nil {
		return nil, errors.New("cannot stage installation key")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(key)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || os.Link(file.Name(), path) != nil {
		return nil, errors.New("cannot persist installation key")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return nil, errors.New("cannot sync installation directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return nil, errors.New("cannot sync installation directory")
	}
	return cryptostore.New(key)
}
