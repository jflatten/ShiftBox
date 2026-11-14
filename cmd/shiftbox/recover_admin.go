package main

import (
	"context"
	"encoding/json"
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

func runRecoverAdmin(ctx context.Context, args []string, out, status io.Writer) error {
	flags := flag.NewFlagSet("recover-admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "config.json", "")
	email := flags.String("email", "", "")
	dex := flags.String("dex-binary", "", "")
	asJSON := flags.Bool("json", false, "")
	parsed := flags.Parse(args)
	report := func(exit int, code, title string) error {
		return recoverAdminOutput(status, *asJSON, exit, code, title)
	}
	const usage = "usage: shiftbox recover-admin --email <existing-email> [--config <path>] [--json]"
	if parsed != nil || flags.NArg() != 0 {
		return report(2, "recovery.invalid_input", usage)
	}
	normalized := strings.ToLower(strings.TrimSpace(*email))
	address, err := mail.ParseAddress(normalized)
	if err != nil || address.Name != "" || address.Address != normalized {
		return report(2, "recovery.invalid_input", usage)
	}
	if *dex == "" {
		*dex, err = defaultDexPath()
		if err != nil {
			return report(5, "recovery.dex_unavailable", "The managed Dex executable is unavailable.")
		}
	}
	if !filepath.IsAbs(*dex) {
		return report(2, "recovery.invalid_input", "Dex binary path must be absolute.")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return report(7, "recovery.invalid_configuration", "Check the local installation configuration.")
	}
	if _, err := os.Lstat(cfg.InstallationDir); err != nil {
		return report(5, "recovery.installation_unavailable", "The local installation is unavailable.")
	}
	lock, err := lockSetup(cfg.InstallationDir)
	if err != nil {
		if errors.Is(err, errInstallationLocked) {
			return report(6, "recovery.installation_locked", "Stop the running ShiftBox process before recovering an administrator.")
		}
		return report(5, "recovery.installation_unavailable", "The protected installation is unavailable.")
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state, err := app.OpenAdministratorRecovery(ctx, cfg)
	if err != nil {
		return report(5, "recovery.installation_unavailable", "Cannot verify the active database or protected installation key.")
	}
	defer state.Close()
	admin, _, _, _, closeDex, err := startControllerDex(ctx, cfg.InstallationDir, *dex, cfg.ManagementOrigin, state.DexPostgres)
	if err != nil {
		return report(5, "recovery.dex_unavailable", "Cannot start the managed identity service.")
	}
	defer closeDex()
	password, err := state.RecoverLocalAdministrator(ctx, normalized, admin)
	if err != nil {
		if errors.Is(err, app.ErrAdministratorRecoveryDenied) {
			return report(4, "recovery.administrator_denied", "Recovery requires an existing active local administrator.")
		}
		return report(1, "recovery.failed", "Cannot recover administrator. Verify the retained administrator and Dex.")
	}
	if _, err := fmt.Fprintln(out, password); err != nil {
		return report(1, "recovery.output_failed", "Temporary password output failed. Rerun local administrator recovery.")
	}
	return report(0, "", "Temporary administrator password issued.")
}

// Recovery keeps the one-time credential on stdout and reports status on stderr.
func recoverAdminOutput(status io.Writer, asJSON bool, exit int, code, title string) error {
	if asJSON {
		result := map[string]any{"apiVersion": "shiftbox.io/cli/v1", "command": "recover-admin", "status": "succeeded"}
		if exit != 0 {
			result["status"] = "failed"
			result["problem"] = map[string]string{"code": code, "title": title}
		}
		if err := json.NewEncoder(status).Encode(result); err != nil {
			return cliExit{1, "Cannot write administrator recovery status."}
		}
	} else if exit != 0 {
		return cliExit{exit, code + " " + title}
	} else if _, err := fmt.Fprintln(status, title); err != nil {
		return cliExit{1, "Cannot write administrator recovery status."}
	}
	if exit != 0 {
		return cliExit{exit, ""}
	}
	return nil
}
