package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
)

var errServeUsage = errors.New("usage: shiftbox serve --config <path>")

func runServe(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		return errServeUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	// Hold the installation lock while loading the database and trust state.
	// Unlike setup, serve never creates an installation directory.
	if info, err := os.Lstat(cfg.InstallationDir); err != nil || !info.IsDir() {
		return app.ErrActiveDatabase
	}
	if cfg.DataProfile == "sqlite" {
		if info, err := os.Lstat(cfg.SQLitePath); err != nil || !info.Mode().IsRegular() {
			return app.ErrActiveDatabase
		}
	}
	lock, err := lockSetup(cfg.InstallationDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	state, err := app.PreflightController(ctx, cfg)
	if err != nil {
		return err
	}
	defer state.Close()
	return serveActiveController(ctx, cfg, state)
}
