package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/web"
	"golang.org/x/term"
)

func runRestore(ctx context.Context, args []string, in io.Reader, out, status io.Writer) error {
	if len(args) == 0 || (args[0] != "validate" && args[0] != "activate") {
		return cliExit{2, "usage: shiftbox restore validate | shiftbox restore activate"}
	}
	activating := args[0] == "activate"
	command := "restore " + args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	archivePath := flags.String("archive", "", "")
	installation := flags.String("installation-id", "", "")
	release := flags.String("release", "", "")
	validationID := flags.String("validation-id", "", "")
	destination := flags.String("destination", "", "")
	postgresConfig := flags.String("postgres-config", "", "")
	standardInput := flags.Bool("password-stdin", false, "")
	asJSON := flags.Bool("json", false, "")
	fail := func(exit int, code, title string) error {
		return restoreOutput(command, out, status, *asJSON, nil, exit, code, title)
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*configPath) ||
		!filepath.IsAbs(*archivePath) || !filepath.IsAbs(*destination) || filepath.Clean(*destination) != *destination ||
		(activating && (!web.ValidUUID(*validationID) || *installation != "" || *release != "")) ||
		(!activating && (!web.ValidUUID(*installation) || *release == "" || *validationID != "")) {
		return fail(2, "restore.invalid_input", "Provide the protected configuration, archive, replacement directory, and the identifiers required by the restore command.")
	}
	info, err := os.Lstat(*configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fail(2, "restore.invalid_input", "Open a private controller configuration file.")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fail(2, "restore.invalid_input", "Open a valid controller configuration.")
	}
	plans := filepath.Join(cfg.InstallationDir, "restore-validations")
	info, err = os.Lstat(cfg.InstallationDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fail(1, "restore.validation_unavailable", "Use a private installation directory.")
	}
	var plan backup.RestorePlan
	if activating {
		lock, err := lockSetup(cfg.InstallationDir)
		if err != nil {
			return fail(6, "restore.installation_busy", "Stop the controller before applying the reviewed restore plan.")
		}
		defer lock.Close()
		plan, err = backup.LoadRestorePlan(plans, *validationID)
		if err != nil {
			return fail(3, "restore.validation_missing", "Open the saved restore validation record.")
		}
		*installation, *release = plan.Validation.Evidence.Metadata.InstallationID, plan.Validation.Evidence.Metadata.Release
	}
	host, err := app.BackupHostID(ctx)
	if err != nil {
		return fail(5, "restore.host_unavailable", "Verify the original controller Mac.")
	}
	schema, err := database.CurrentSchemaVersion(cfg.DataProfile)
	if err != nil {
		return fail(7, "restore.incompatible", "Use the release that matches the archive.")
	}
	expected := backup.RestoreRequirements{Metadata: backup.ArchiveMetadata{InstallationID: *installation,
		Release: *release, Profile: cfg.DataProfile, Schema: schema, HostID: host}}
	keyPath := filepath.Join(cfg.InstallationDir, "installation.key")
	if _, err := os.Lstat(keyPath); !errors.Is(err, os.ErrNotExist) {
		expected.MountedKey, err = cryptostore.LoadKey(keyPath)
		if err != nil {
			return fail(7, "restore.key_invalid", "Verify the retained installation key.")
		}
		defer clear(expected.MountedKey)
	}
	target, err := restoreTarget(cfg, *destination, *postgresConfig)
	if err != nil {
		return fail(2, "restore.invalid_target", "Provide a private replacement directory and scoped PostgreSQL settings if required.")
	}
	archive, err := openRestoreInput(*archivePath)
	if err != nil {
		return fail(3, "restore.archive_missing", "Open the selected management archive.")
	}
	defer archive.Close()
	password, err := readRecoveryPassphrase(in, status, *standardInput, false)
	if err != nil {
		return fail(2, "restore.password_input", "Read the passphrase from a terminal or explicitly use --password-stdin.")
	}
	if activating {
		stage, err := backup.StageRestore(ctx, filepath.Dir(*destination), plan, target.digest, archive, password, expected, target.check)
		if err != nil {
			return fail(7, "restore.revalidation_failed", "Verify the archive, passphrase, retained key, and empty replacement against the saved validation.")
		}
		defer os.RemoveAll(stage)
		if err := prepareRestoreConfiguration(stage, *destination, cfg.DataProfile, target.postgres, expected.MountedKey); err != nil {
			return fail(7, "restore.invalid_configuration", "Verify the archived controller configuration and retained key.")
		}
		review, err := activateRestore(ctx, stage, *destination, expected.Metadata, plan.ID, target.postgres)
		if err != nil {
			if target.postgres != nil {
				return fail(1, "restore.activation_failed", "Keep the PostgreSQL replacement offline. If either schema was loaded, provision an empty replacement with separate owners and validate again before retrying.")
			}
			return fail(1, "restore.activation_failed", "Cannot publish the replacement in recovery review. Inspect the replacement before retrying.")
		}
		result := restoreCLIResult{ValidationID: plan.ID, RecoveryReviewID: review,
			Configuration: filepath.Join(*destination, "configuration.json"), Release: *release, Profile: cfg.DataProfile,
			Schema: schema, KeyDisposition: plan.Validation.KeyDisposition,
			ReviewSteps: []string{"identities", "provider", "authorization_retention", "activate"}}
		return restoreOutput(command, out, status, *asJSON, &result, 0, "", "")
	}
	validation, err := backup.ValidateRestore(ctx, archive, password, expected, target.check)
	if err != nil {
		code, title := "restore.invalid_archive", "Verify the archive integrity and passphrase."
		switch {
		case errors.Is(err, backup.ErrRestoreTarget):
			code, title = "restore.invalid_target", "Choose an empty, owned replacement target."
		case errors.Is(err, backup.ErrRestoreCompatibility):
			code, title = "restore.incompatible", "Use the original Mac and the matching installation, release, and schema."
		case errors.Is(err, backup.ErrRestoreKey):
			code, title = "restore.key_invalid", "Verify the retained installation key."
		}
		return fail(7, code, title)
	}
	if err := os.Mkdir(plans, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail(1, "restore.validation_unavailable", "Cannot record restore validation in the installation directory.")
	}
	plan, err = backup.RecordRestorePlan(ctx, plans, target.digest, validation)
	if err != nil {
		return fail(1, "restore.validation_unavailable", "Cannot save the private restore validation record.")
	}
	result := restoreCLIResult{ValidationID: plan.ID, Release: validation.Evidence.Metadata.Release,
		Profile: validation.Evidence.Metadata.Profile, Schema: validation.Evidence.Metadata.Schema,
		KeyDisposition: validation.KeyDisposition, ReviewSteps: []string{"identities", "provider", "authorization_retention", "activate"}}
	if cfg.DataProfile == "postgres" {
		result.OwnedSchemas = []string{"shiftbox", "dex"}
	}
	return restoreOutput(command, out, status, *asJSON, &result, 0, "", "")
}

func prepareRestoreConfiguration(stage, destination, profile string, target *postgresReplacement, retainedKey []byte) error {
	path := filepath.Join(stage, "configuration.json")
	cfg, err := config.Load(path)
	if err != nil || cfg.DataProfile != profile {
		return backup.ErrRestoreCompatibility
	}
	cfg.InstallationDir = destination
	if target == nil {
		cfg.SQLitePath = filepath.Join(destination, "application.db")
	} else {
		application, err := url.Parse(target.database.ApplicationURI)
		if err != nil {
			return backup.ErrRestoreTarget
		}
		q := application.Query()
		ca := target.caPath
		if ca == "" {
			ca = "system"
		}
		q.Set("sslrootcert", ca)
		application.RawQuery = q.Encode()
		cfg.PostgresURI = application.String()
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeRestoreFile(path, encoded, os.O_TRUNC); err != nil {
		return err
	}
	if retainedKey != nil {
		// The replacement uses the matching retained key. Leave the original
		// key file in place and provision only the new installation directory.
		return writeRestoreFile(filepath.Join(stage, "installation.key"), retainedKey, os.O_CREATE|os.O_EXCL)
	}
	return nil
}

func writeRestoreFile(path string, data []byte, flags int) error {
	file, err := os.OpenFile(path, flags|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

type restoreReplacement struct {
	digest   string
	check    func(context.Context) error
	postgres *postgresReplacement
}

type postgresReplacement struct {
	database database.PostgresSetupTarget
	caPath   string
}

func restoreTarget(cfg config.Config, destination, postgresPath string) (restoreReplacement, error) {
	checkLocal := func() error { return checkRestoreDirectory(destination) }
	if cfg.DataProfile == "sqlite" && postgresPath == "" {
		return restoreReplacement{digest: restoreDigest("sqlite", destination), check: func(context.Context) error { return checkLocal() }}, nil
	}
	if cfg.DataProfile != "postgres" || postgresPath == "" {
		return restoreReplacement{}, backup.ErrRestoreTarget
	}
	profile, err := readSetupPostgresProfile(postgresPath)
	if err != nil {
		return restoreReplacement{}, err
	}
	return postgresRestoreReplacement(destination, cfg.PostgresURI, profile, checkLocal)
}

func postgresRestoreReplacement(destination, applicationURI string, profile *app.PostgresProfileSetup, checkLocal func() error) (restoreReplacement, error) {
	application, err := url.Parse(applicationURI)
	if err != nil {
		return restoreReplacement{}, backup.ErrRestoreTarget
	}
	dex, err := url.Parse(profile.DexURI)
	if err != nil {
		return restoreReplacement{}, backup.ErrRestoreTarget
	}
	// Credentials may be rotated. Bind host, database and role identity.
	digest := restoreDigest("postgres", destination, application.Redacted(), dex.Redacted(), profile.CAFile)
	target := database.PostgresSetupTarget{ApplicationURI: applicationURI, DexURI: profile.DexURI, Roots: profile.Roots}
	return restoreReplacement{digest: digest, postgres: &postgresReplacement{database: target, caPath: profile.CAFile}, check: func(ctx context.Context) error {
		if err := checkLocal(); err != nil {
			return err
		}
		return backup.ValidatePostgresReplacement(ctx, target)
	}}, nil
}

func checkRestoreDirectory(destination string) error {
	parent, err := os.Lstat(filepath.Dir(destination))
	if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return backup.ErrRestoreTarget
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return backup.ErrRestoreTarget
	}
	return nil

}

func restoreDigest(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func openRestoreInput(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, backup.ErrRestoreTarget
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	found, err := file.Stat()
	if err != nil || !os.SameFile(info, found) {
		file.Close()
		return nil, backup.ErrRestoreTarget
	}
	return file, nil
}

func readRecoveryPassphrase(in io.Reader, status io.Writer, standardInput, confirm bool) (string, error) {
	invalid := errors.New("invalid archive passphrase input")
	input := bufio.NewReaderSize(in, 1026)
	read := func(prompt string) ([]byte, error) {
		if standardInput {
			line, err := input.ReadSlice('\n')
			if err != nil && err != io.EOF {
				return nil, invalid
			}
			password := bytes.Clone(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")))
			clear(line)
			return password, nil
		}
		terminal, ok := in.(*os.File)
		if !ok || !term.IsTerminal(int(terminal.Fd())) {
			return nil, invalid
		}
		fmt.Fprint(status, prompt)
		line, err := term.ReadPassword(int(terminal.Fd()))
		fmt.Fprintln(status)
		return line, err
	}
	password, err := read("Archive passphrase: ")
	defer clear(password)
	if err != nil || len(password) == 0 || len(password) > 1024 {
		return "", invalid
	}
	if confirm {
		repeated, err := read("Repeat archive passphrase: ")
		defer clear(repeated)
		if err != nil || subtle.ConstantTimeCompare(password, repeated) != 1 {
			return "", invalid
		}
	}
	return string(password), nil
}

type restoreCLIResult struct {
	RecoveryReviewID string   `json:"recoveryReviewId,omitempty"`
	Configuration    string   `json:"configuration,omitempty"`
	ValidationID     string   `json:"validationId"`
	Release          string   `json:"release"`
	Profile          string   `json:"profile"`
	Schema           int64    `json:"schema"`
	KeyDisposition   string   `json:"keyDisposition"`
	OwnedSchemas     []string `json:"ownedSchemas,omitempty"`
	ReviewSteps      []string `json:"reviewSteps"`
}

func restoreOutput(command string, out, status io.Writer, asJSON bool, result *restoreCLIResult, exit int, code, title string) error {
	if asJSON {
		envelope := struct {
			APIVersion string            `json:"apiVersion"`
			Command    string            `json:"command"`
			Status     string            `json:"status"`
			Result     *restoreCLIResult `json:"result"`
			Problem    *web.Problem      `json:"problem"`
		}{APIVersion: "shiftbox.io/cli/v1", Command: command, Status: "succeeded", Result: result}
		if exit != 0 {
			statusCode := map[int]int{1: 500, 2: 400, 3: 404, 5: 503, 6: 409, 7: 422}[exit]
			envelope.Status = "failed"
			envelope.Problem = &web.Problem{Type: "about:blank", Status: statusCode, Code: code, Title: title}
		}
		if err := json.NewEncoder(out).Encode(envelope); err != nil {
			return cliExit{1, "Cannot write restore result."}
		}
	} else if result != nil && result.RecoveryReviewID != "" {
		fmt.Fprintf(status, "Replacement entered recovery review. Start ShiftBox with %s.\n", result.Configuration)
		fmt.Fprintln(out, result.RecoveryReviewID)
	} else if result != nil {
		fmt.Fprintf(status, "Validated %s archive for %s, schema %d.\n", result.Profile, result.Release, result.Schema)
		fmt.Fprintf(status, "Key disposition %s.\n", result.KeyDisposition)
		if len(result.OwnedSchemas) != 0 {
			fmt.Fprintf(status, "Owned schemas %s.\n", strings.Join(result.OwnedSchemas, ", "))
		}
		fmt.Fprintln(status, "Review identities, provider inventory, authorization, and retention before activating.")
		fmt.Fprintln(out, result.ValidationID)
	}
	if exit != 0 {
		return cliExit{exit, title}
	}
	return nil
}
