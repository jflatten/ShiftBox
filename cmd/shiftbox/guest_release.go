package main

import (
	"context"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/release"
)

// Cloud configuration may live in a temporary directory. Published artifacts
// belong to the durable installation and must survive controller replacements.
func controllerGuestUpdateLoader(cfg config.Config, protocol string) func(context.Context, string) (release.GuestUpdate, error) {
	root := filepath.Join(cfg.InstallationDir, "releases")
	return func(ctx context.Context, id string) (release.GuestUpdate, error) {
		return release.ReadLocalGuestUpdate(ctx, root, id, protocol)
	}
}
