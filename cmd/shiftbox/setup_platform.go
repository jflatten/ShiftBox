package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

type setupPlatform struct {
	installation, folder string
	runtime              config.ControllerRuntime
	cloud                *app.CloudSetup
}

func readSetupPlatform(dir, configPath, folder string) (*setupPlatform, error) {
	if configPath == "" && folder == "" {
		return nil, nil
	}
	if configPath == "" {
		return nil, errors.New("platform setup requires --config and an absolute --platform-folder")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(cfg.InstallationDir) != filepath.Clean(dir) || cfg.Runtime == nil {
		return nil, errors.New("platform setup requires runtime settings for this installation directory")
	}
	if err := checkControllerRuntime(*cfg.Runtime); err != nil {
		return nil, err
	}
	platform := &setupPlatform{installation: dir, folder: folder, runtime: *cfg.Runtime}
	if cfg.Runtime.Provider != "ec2" {
		if !filepath.IsAbs(folder) {
			return nil, errors.New("platform setup requires an absolute --platform-folder")
		}
		return platform, nil
	}
	suffix, valid := strings.CutPrefix(cfg.Runtime.GuestHost, "guest.")
	if !valid || cfg.DataProfile != "postgres" || folder != "" {
		return nil, errors.New("EC2 setup requires PostgreSQL and the deployed guest DNS name")
	}
	ide := "{workspace}.ide." + suffix
	preview := "{service}--{workspace}.preview." + suffix
	if _, err := config.NewServiceHosts(cfg.ManagementOrigin, ide, preview); err != nil {
		return nil, errors.New("EC2 setup requires valid deployed browser names")
	}
	platform.cloud = &app.CloudSetup{ApplicationURI: cfg.PostgresURI, ManagementOrigin: cfg.ManagementOrigin,
		IDEHostPattern: ide, PreviewHostPattern: preview}
	return platform, nil
}

func (p setupPlatform) validator(db *sql.DB, installationID string) func(context.Context, string, string, string) error {
	return func(ctx context.Context, actor, current, previous string) error {
		store := release.NewStore(db)
		catalog := release.NewCatalog(store, filepath.Join(p.installation, "platform-artifacts"))
		machines, _, err := controllerProvider(p.installation, p.runtime, &app.ControllerState{DB: db})
		if err != nil {
			return err
		}
		caps, err := machines.Capabilities(ctx)
		if err != nil {
			return err
		}
		validationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(installationID+":boot-validation")).String()
		marker := uuid.NewSHA1(uuid.NameSpaceOID, []byte(installationID+":boot-validation-owner")).String()
		return p.importPlatform(ctx, store, catalog, machines, caps,
			provider.WorkspaceRef{InstallationID: installationID, WorkspaceID: validationID, Generation: 1, OwnershipMarker: marker},
			actor, p.folder, current, previous, true)
	}
}

func (p setupPlatform) importPlatform(ctx context.Context, store *release.Store, catalog *release.Catalog, machines app.PlatformBootProvider,
	caps provider.Capabilities, ref provider.WorkspaceRef, actor, folder, current, previous string, makeDefault bool,
) error {
	enrollHost, _, _ := net.SplitHostPort(p.runtime.EnrollmentAddress)
	pollHost, _, _ := net.SplitHostPort(p.runtime.PollAddress)
	listeners := app.GuestListenerConfig{PublicHost: p.runtime.GuestHost,
		EnrollmentAddress: net.JoinHostPort(enrollHost, "0"), PollAddress: net.JoinHostPort(pollHost, "0")}
	if !caps.Create || !caps.Start || !caps.Stop || !caps.Delete {
		return provider.ErrCapabilityUnsupported
	}
	entry, err := catalog.ImportLocal(ctx, actor, time.Now(), folder)
	if err != nil {
		return err
	}
	manifest := entry.Artifact.Manifest
	if !caps.SupportsArchitecture(manifest.Architecture) {
		return provider.ErrCapabilityUnsupported
	}
	if manifest.ProtocolVersion != guestProtocol || (manifest.Guest.Version != current && manifest.Guest.Version != previous) {
		return release.ErrCatalogConflict
	}
	req := provider.CreateRequest{Ref: ref, OperationID: ref.WorkspaceID, ArtifactID: entry.ID,
		Release: manifest.ReleaseID, NetworkMode: "shared", Resources: caps.Minimum}
	if err := catalog.ValidateImport(ctx, entry.ID, func(ctx context.Context, artifact release.Artifact) error {
		return app.ValidatePlatformBoot(ctx, p.installation, machines, req, artifact, listeners)
	}); err != nil {
		return err
	}
	if makeDefault {
		return store.SelectDefault(ctx, entry.ID, []string{guestProtocol})
	}
	return nil
}
