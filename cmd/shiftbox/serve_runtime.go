package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

const guestProtocol = "1"

func serveActiveController(ctx context.Context, cfg config.Config, state *app.ControllerState, configPath string) error {
	if cfg.Runtime == nil {
		return errors.New("serve requires a runtime configuration for HTTPS, guest listeners, and UTM")
	}
	settings := *cfg.Runtime
	if err := checkControllerRuntime(settings); err != nil {
		return err
	}
	active := state.Installation
	if active.ActiveRelease == "" || active.MinimumGuestRelease == "" {
		return errors.New("installation is missing its active or minimum guest release")
	}
	compatibility := web.GuestCompatibility{
		Protocol: guestProtocol, CurrentRelease: active.ActiveRelease, PreviousRelease: active.MinimumGuestRelease,
	}
	var github *identity.GitHub
	githubApp, err := readControllerGitHub(settings.GitHubAppFile)
	if err != nil {
		return err
	}
	if githubApp != nil {
		github, err = state.GitHub(githubApp.ClientID, githubApp.ClientSecret, githubApp.InstallationID)
		if err != nil {
			return err
		}
	}
	assets, err := browserAssets()
	if err != nil {
		return err
	}
	if assets == nil {
		return errors.New("serve requires embedded browser assets; build the controller with make build")
	}
	binary := settings.DexBinary
	if binary == "" {
		binary, err = defaultDexPath()
		if err != nil {
			return err
		}
	}

	machines, media, err := controllerProvider(cfg.InstallationDir, settings, state)
	if err != nil {
		return err
	}
	caps, err := machines.Capabilities(ctx)
	if err != nil {
		return errors.New("cannot verify UTM capabilities; check the supported UTM version and macOS automation access")
	}
	if !caps.Create || !caps.Start || !caps.Stop || !caps.Delete || caps.Architecture != "amd64" {
		return errors.New("UTM lacks required amd64 workspace lifecycle capabilities")
	}
	checkpoint := &backup.WriteFence{}
	journal := operation.NewJournal(state.DB, time.Now, uuid.NewString)
	tunnels := &proxy.Tunnels{Registry: &proxy.Registry{}}
	origin := active.Settings.ManagementOrigin
	passwords, login, dex, dexDone, stopDex, err := startControllerDex(ctx, cfg.InstallationDir, binary, origin, state.DexPostgres)
	if err != nil {
		return err
	}
	defer stopDex()
	signIn, reauthorize, err := app.NewActiveIdentityHandlers(state.DB, active.InstallationID, login, tunnels.Registry,
		func(ctx context.Context) (identity.ConnectorIdentity, error) {
			return app.CurrentExternalConnector(ctx, state.DB)
		}, time.Now, uuid.NewString)
	if err != nil {
		return err
	}
	policy := web.WorkspaceLaunchPolicy{Capabilities: caps, Protocols: []string{guestProtocol}}
	services, err := app.NewManagementServices(state, journal, tunnels.Registry, policy, github, passwords, time.Now)
	if err != nil {
		return err
	}
	connectors, ok := passwords.Client.(identity.ExternalConnectorAPI)
	if !ok {
		return errors.New("managed Dex connector administration is unavailable")
	}
	configureExternal := app.ExternalConfigurationHandler(state.DB, origin, tunnels.Registry,
		identity.ExternalConnectorAdmin{Client: connectors}, time.Now)
	services.Handlers[reauthorize.Route.OperationID] = reauthorize
	services.Handlers[configureExternal.Route.OperationID] = configureExternal
	artifactStore := release.NewStore(state.DB)
	catalog := release.NewCatalog(artifactStore, filepath.Join(cfg.InstallationDir, "platform-artifacts"))
	platform := setupPlatform{installation: cfg.InstallationDir, runtime: settings}
	imports := app.NewPlatformImport(state.DB, journal, origin, active.InstallationID, time.Now,
		func(ctx context.Context, id, actor, folder string, makeDefault bool) error {
			// Stable operation identity lets a retry reconcile its validation VM.
			validationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(active.InstallationID+":platform-import:"+id)).String()
			marker := uuid.NewSHA1(uuid.NameSpaceOID, []byte(validationID+":owner")).String()
			ref := provider.WorkspaceRef{InstallationID: active.InstallationID, WorkspaceID: validationID, Generation: 1, OwnershipMarker: marker}
			return platform.importPlatform(ctx, artifactStore, catalog, machines, caps, ref,
				actor, folder, active.ActiveRelease, active.MinimumGuestRelease, makeDefault)
		})
	importRoute := imports.Handler()
	services.Handlers[importRoute.Route.OperationID] = importRoute
	preflightRoute, preflightWorker := web.MigrationPreflightRoute(state.DB, journal, origin, active.InstallationID, time.Now,
		func(ctx context.Context, id string) (release.MigrationReport, error) {
			manifest, err := release.ReadLocalMigrationRelease(filepath.Join(filepath.Dir(configPath), "releases"), id)
			if err != nil {
				return release.MigrationReport{}, err
			}
			current, _ := machines.Capabilities(ctx)
			return assessControllerMigration(ctx, cfg, state, manifest, current)
		})
	services.Handlers[preflightRoute.Route.OperationID] = preflightRoute
	preflightWorker.EnterActivity = checkpoint.Enter
	metadata, err := state.BackupMetadata(ctx)
	if err != nil {
		return err
	}
	backups := backup.NewService(state.DB, journal, metadata)
	if err := backups.InterruptIncomplete(ctx); err != nil {
		return err
	}
	backupJobs := make(chan web.BackupJob, 1)
	backupRoute := web.BackupRoute(state.DB, backups, origin, active.InstallationID, time.Now,
		func(job web.BackupJob) { backupJobs <- job })
	services.Handlers[backupRoute.Route.OperationID] = backupRoute
	api, err := web.NewActiveManagementRouter(services.Session, services.Handlers, services.GitHubCallback)
	if err != nil {
		return err
	}
	site, err := app.NewManagementSite(api, signIn, dex, assets)
	if err != nil {
		return err
	}
	browser, err := app.ListenBrowser(settings.BrowserAddress, state, site, tunnels, time.Now, checkpoint)
	if err != nil {
		return err
	}
	defer browser.Close()
	workspaces, err := app.ListenWorkspaceRuntime(state, app.GuestListenerConfig{
		EnrollmentAddress: settings.EnrollmentAddress, PollAddress: settings.PollAddress,
		TunnelAddress: settings.TunnelAddress, PublicHost: settings.GuestHost, Tunnels: tunnels, GitHub: github, Checkpoint: checkpoint,
	}, compatibility, journal, machines, media, time.Now)
	if err != nil {
		return err
	}
	defer workspaces.Close()
	err = app.ServeController(ctx, dexDone, browser.Serve, workspaces.Serve, func(ctx context.Context) error {
		return app.RunHistoryRetention(ctx, state.DB, cfg, checkpoint)
	}, func(ctx context.Context) error {
		return imports.Run(ctx, checkpoint.Enter)
	}, func(ctx context.Context) error {
		return preflightWorker.Run(ctx, time.Second)
	}, func(ctx context.Context) error {
		return web.RunBackupJobs(ctx, backupJobs, backups, func(ctx context.Context, dir string) (map[string]string, error) {
			return state.CaptureBackupCheckpoint(ctx, dir, configPath, cfg, checkpoint)
		})
	})
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

func controllerProvider(dir string, settings config.ControllerRuntime, state *app.ControllerState) (*utm.Provider, string, error) {
	if info, err := os.Stat(settings.UTMStorage); err != nil || !info.IsDir() {
		return nil, "", errors.New("UTM storage must be the existing UTM Documents directory")
	}
	staging := filepath.Join(dir, "vm-staging")
	media := filepath.Join(dir, "enrollment-media")
	for _, path := range []string{staging, media} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, "", errors.New("cannot create private workspace runtime storage")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return nil, "", errors.New("workspace runtime storage must be private")
		}
	}
	catalog := release.NewCatalog(release.NewStore(state.DB), filepath.Join(dir, "platform-artifacts"))
	machines, err := utm.NewProvider(catalog, staging, settings.UTMStorage,
		providerResources(settings.UTMMinimum), providerResources(settings.UTMMaximum))
	return machines, media, err
}

func checkControllerRuntime(settings config.ControllerRuntime) error {
	seen := make(map[string]bool, 4)
	addresses := []string{settings.BrowserAddress, settings.EnrollmentAddress, settings.PollAddress, settings.TunnelAddress}
	for _, address := range addresses {
		_, port, err := net.SplitHostPort(address)
		number, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || portErr != nil || number == 0 || seen[address] {
			return errors.New("runtime requires four distinct listener addresses with explicit ports")
		}
		seen[address] = true
	}
	if settings.GuestHost == "" {
		return errors.New("runtime requires a guest-reachable TLS hostname or IP address")
	}
	if !filepath.IsAbs(settings.UTMStorage) || (settings.DexBinary != "" && !filepath.IsAbs(settings.DexBinary)) {
		return errors.New("runtime requires absolute paths for UTM storage and an explicitly configured Dex binary")
	}
	minimum, maximum := settings.UTMMinimum, settings.UTMMaximum
	if minimum.CPU < 1 || minimum.MemoryMiB < 1 || minimum.DiskMiB < 1 ||
		maximum.CPU < minimum.CPU || maximum.MemoryMiB < minimum.MemoryMiB || maximum.DiskMiB < minimum.DiskMiB ||
		maximum.MemoryMiB > math.MaxInt64/(1<<20) || maximum.DiskMiB > math.MaxInt64/(1<<20) {
		return errors.New("runtime requires positive and ordered UTM resource bounds")
	}
	return nil
}

func providerResources(resources config.VMResources) provider.Resources {
	return provider.Resources{CPU: resources.CPU, MemoryBytes: resources.MemoryMiB << 20, DiskBytes: resources.DiskMiB << 20}
}

type controllerGitHubApp struct {
	ClientID       string `json:"clientId"`
	ClientSecret   string `json:"clientSecret"`
	InstallationID int64  `json:"installationId"`
}

func readControllerGitHub(path string) (*controllerGitHubApp, error) {
	if path == "" {
		return nil, nil
	}
	const limit = 32 << 10
	invalid := errors.New("GitHub App configuration must be a valid private file")
	info, err := os.Lstat(path)
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	var app controllerGitHubApp
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&app); err != nil || app.ClientID == "" || app.ClientSecret == "" || app.InstallationID < 1 {
		return nil, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalid
	}
	return &app, nil
}
