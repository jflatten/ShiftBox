package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

type cliExit struct {
	code    int
	problem string
}

func (e cliExit) Error() string { return e.problem }

func runMigrate(ctx context.Context, args []string, out, status io.Writer) error {
	if len(args) == 0 || args[0] != "preflight" {
		return cliExit{2, "usage: shiftbox migrate preflight --config <path> --release <manifest>"}
	}
	flags := flag.NewFlagSet("migrate preflight", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cfgPath := flags.String("config", "", "")
	manifestPath := flags.String("release", "", "")
	asJSON := flags.Bool("json", false, "")
	failure := func(exit int, code, title string) error {
		return migrationOutput(out, status, *asJSON, nil, web.Problem{Code: code, Title: title}, exit)
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *cfgPath == "" || *manifestPath == "" {
		return failure(2, "migration.invalid_input", "Specify the installation configuration and release manifest.")
	}
	file, err := os.Open(*manifestPath)
	if err != nil {
		return failure(2, "migration.invalid_release", "Open a valid pinned release manifest.")
	}
	manifest, err := release.ReadMigrationRelease(file)
	file.Close()
	if err != nil {
		return failure(2, "migration.invalid_release", "Open a valid pinned release manifest.")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil || cfg.Runtime == nil {
		return failure(2, "migration.invalid_configuration", "Use the active installation’s runtime configuration.")
	}
	state, err := app.PreflightController(ctx, cfg)
	if err != nil {
		return failure(7, "migration.installation_unverified", "Verify the active database, installation key, and HTTPS certificates.")
	}
	defer state.Close()
	report, err := assessControllerMigration(ctx, cfg, state, manifest, migrationCapabilities(ctx, cfg, state))
	if err != nil {
		return failure(5, "migration.guest_inventory_unavailable", "Read the retained workspace inventory before continuing.")
	}
	if !report.Ready {
		return migrationOutput(out, status, *asJSON, &report, web.Problem{Code: "migration.blocked", Title: "Migration preflight is blocked."}, 7)
	}
	return migrationOutput(out, status, *asJSON, &report, web.Problem{}, 0)
}

func migrationCapabilities(ctx context.Context, cfg config.Config, state *app.ControllerState) provider.Capabilities {
	caps := provider.Capabilities{}
	if cfg.Runtime != nil && checkControllerRuntime(*cfg.Runtime) == nil {
		runtime := *cfg.Runtime
		catalog := release.NewCatalog(release.NewStore(state.DB), filepath.Join(cfg.InstallationDir, "platform-artifacts"))
		storage, statErr := os.Stat(runtime.UTMStorage)
		machines, providerErr := utm.NewProvider(catalog, filepath.Join(cfg.InstallationDir, "vm-staging"), runtime.UTMStorage,
			providerResources(runtime.UTMMinimum), providerResources(runtime.UTMMaximum))
		if providerErr == nil && statErr == nil && storage.IsDir() {
			caps, _ = machines.Capabilities(ctx)
		}
	}
	return caps
}

func assessControllerMigration(ctx context.Context, cfg config.Config, state *app.ControllerState, manifest release.MigrationRelease, caps provider.Capabilities) (release.MigrationReport, error) {
	now := time.Now()
	evidence := release.MigrationEvidence{
		ProviderCompatible: manifest.ProtocolVersion == guestProtocol && caps.Version == manifest.UTM.Version &&
			caps.Architecture == "amd64" && caps.Create && caps.Start && caps.Stop && caps.Delete,
		StorageWritable: migrationStorageWritable(cfg.InstallationDir),
		ToolsCompatible: migrationDexMatches(cfg.Runtime.DexBinary, manifest.Dex) && migrationBackupTools(ctx, state, cfg.DataProfile),
	}
	evidence, err := state.MigrationEvidence(ctx, evidence, guestProtocol, now)
	if err != nil {
		return release.MigrationReport{}, err
	}
	return release.AssessMigration(manifest.ReleaseID, evidence, now), nil
}

func migrationBackupTools(ctx context.Context, state *app.ControllerState, profile string) bool {
	if profile == "sqlite" {
		return true
	}
	var major int
	if profile != "postgres" || state.DB.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&major) != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		output, err := exec.CommandContext(ctx, tool, "--version").Output()
		version, ok := strings.CutPrefix(string(output), tool+" (PostgreSQL) ")
		if err != nil || !ok || !strings.HasPrefix(version, strconv.Itoa(major)+".") {
			return false
		}
	}
	return true
}

func migrationStorageWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".migration-preflight-")
	if err != nil {
		return false
	}
	return errors.Join(file.Close(), os.Remove(file.Name())) == nil
}

func migrationDexMatches(path string, expected release.ArtifactComponent) bool {
	if expected.Version != identity.DexVersion {
		return false
	}
	if path == "" {
		var err error
		path, err = defaultDexPath()
		if err != nil {
			return false
		}
	}
	if !filepath.IsAbs(path) {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return false
	}
	digest := sha256.New()
	_, err = io.Copy(digest, file)
	return err == nil && hex.EncodeToString(digest.Sum(nil)) == expected.SHA256
}

func migrationOutput(out, status io.Writer, asJSON bool, report *release.MigrationReport, problem web.Problem, exit int) error {
	if asJSON {
		result := struct {
			APIVersion string                   `json:"apiVersion"`
			Command    string                   `json:"command"`
			Status     string                   `json:"status"`
			Result     *release.MigrationReport `json:"result"`
			Problem    *web.Problem             `json:"problem"`
		}{APIVersion: "shiftbox.io/cli/v1", Command: "migrate preflight", Status: "succeeded", Result: report}
		if exit != 0 {
			problem.Type = "about:blank"
			problem.Status = 409
			if exit == 2 {
				problem.Status = 400
			} else if exit == 5 {
				problem.Status = 503
			}
			result.Status, result.Problem = "failed", &problem
		}
		if err := json.NewEncoder(out).Encode(result); err != nil {
			return cliExit{1, "Cannot write migration preflight result."}
		}
	} else if report != nil {
		for _, blocker := range report.Blockers {
			fmt.Fprintf(status, "%s %s\n", blocker.Code, blocker.Detail)
		}
		if exit == 0 {
			fmt.Fprintln(status, "Migration preflight passed.")
		}
	}
	if exit != 0 {
		return cliExit{exit, problem.Title}
	}
	return nil
}
