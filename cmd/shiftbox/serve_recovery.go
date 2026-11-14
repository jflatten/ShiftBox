package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

var errRecoveryActivated = errors.New("recovery review activated")

func serveRecoveryController(ctx context.Context, cfg config.Config, state *app.ControllerState,
	binary string, assets http.Handler, machines provider.Provider) error {
	active := state.Installation
	origin := active.Settings.ManagementOrigin
	journal := operation.NewJournal(state.DB, time.Now, uuid.NewString)
	registry := &proxy.Registry{}
	sessions := web.NewSessionHandler(state.DB, origin, registry, time.Now)
	sessions.RecoveryReviewID = active.RecoveryReviewID
	_, login, dex, dexDone, closeDex, err := startControllerDex(ctx, cfg.InstallationDir, binary, origin, state.DexPostgres)
	if err != nil {
		return err
	}
	defer closeDex()
	signIn, err := app.NewActiveLoginHandler(state.DB, active.InstallationID, login, time.Now, uuid.NewString)
	if err != nil {
		return err
	}

	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	api := http.NewServeMux()
	api.Handle("/api/v1/session", sessions)
	for _, handler := range web.RecoveryRoutes(state.DB, journal, origin, active.RecoveryReviewID, time.Now,
		func(ctx context.Context) ([]backup.RecoveryWorkspace, error) {
			return backup.ObserveRecoveryWorkspaces(ctx, state.DB, active.InstallationID, machines.Inspect)
		}, func() { stop(errRecoveryActivated) }) {
		api.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, handler)
	}
	site, err := app.NewManagementSite(web.WithRequestID(api), signIn, dex, assets)
	if err != nil {
		return err
	}
	browser, err := app.ListenBrowser(cfg.Runtime.BrowserAddress, state, site, &proxy.Tunnels{Registry: registry}, time.Now, nil)
	if err != nil {
		return err
	}
	defer browser.Close()
	wait := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	err = app.ServeController(runCtx, dexDone, browser.Serve, wait, wait)
	if errors.Is(context.Cause(runCtx), errRecoveryActivated) {
		return errRecoveryActivated
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}
