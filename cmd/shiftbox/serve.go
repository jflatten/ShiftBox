package main

import (
	"context"
	"errors"
	"flag"
	"io"

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
	runtime, err := app.PreflightController(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	lock, err := lockSetup(cfg.InstallationDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	return errors.New("controller startup requires management route composition")
}
