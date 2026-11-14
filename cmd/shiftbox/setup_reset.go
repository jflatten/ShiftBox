package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
)

func resetSetupPassword(ctx context.Context, dir, dex, email string, out io.Writer) error {
	for _, name := range []string{"bootstrap.db", "dex.db", "bootstrap-tls.pem", "installation.key"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("temporary password reset requires an existing local setup")
		}
	}
	lock, err := lockSetup(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	key, err := cryptostore.LoadKey(filepath.Join(dir, "installation.key"))
	if err != nil {
		return err
	}
	clear(key)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "bootstrap.db")})
	if err != nil {
		return err
	}
	defer db.Close()
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		return err
	}
	admin, _, _, closeDex, err := startSetupDex(ctx, dir, dex, "https://127.0.0.1:9999", cert, nil)
	if err != nil {
		return err
	}
	defer closeDex()
	password, err := app.ResetSetupTemporaryPassword(ctx, db, email, admin, time.Now)
	if err != nil {
		return errors.New("cannot reset temporary password; verify the existing setup administrator and Dex")
	}
	if _, err := fmt.Fprintln(out, password); err != nil {
		return errors.New("temporary password output failed; rerun the protected local reset")
	}
	return nil
}
