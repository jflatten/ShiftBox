package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
)

func runRecoverAdmin(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("recover-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config.json", "")
	email := flags.String("email", "", "")
	dex := flags.String("dex-binary", "", "")
	const usage = "usage: shiftbox recover-admin --email <existing-email> [--config <path>]"
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New(usage)
	}
	normalized := strings.ToLower(strings.TrimSpace(*email))
	address, err := mail.ParseAddress(normalized)
	if err != nil || address.Name != "" || address.Address != normalized {
		return errors.New(usage)
	}
	if *dex == "" {
		*dex, err = defaultDexPath()
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(*dex) {
		return errors.New("Dex binary path must be absolute")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(cfg.InstallationDir); err != nil {
		return app.ErrActiveDatabase
	}
	lock, err := lockSetup(cfg.InstallationDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state, err := app.OpenAdministratorRecovery(ctx, cfg)
	if err != nil {
		return err
	}
	defer state.Close()
	admin, _, _, _, closeDex, err := startControllerDex(ctx, cfg.InstallationDir, *dex, cfg.ManagementOrigin, state.DexPostgres)
	if err != nil {
		return err
	}
	defer closeDex()
	password, err := state.RecoverLocalAdministrator(ctx, normalized, admin)
	if err != nil {
		return errors.New("cannot recover administrator; verify the existing local administrator and Dex")
	}
	if _, err := fmt.Fprintln(out, password); err != nil {
		return errors.New("temporary password output failed; rerun local administrator recovery")
	}
	return nil
}
