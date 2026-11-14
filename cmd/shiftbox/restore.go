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
	if len(args) == 0 || args[0] != "validate" {
		return cliExit{2, "usage: shiftbox restore validate --archive <path> --config <path> --installation-id <id> --release <id> --destination <path>"}
	}
	const command = "restore validate"
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	archivePath := flags.String("archive", "", "")
	installation := flags.String("installation-id", "", "")
	release := flags.String("release", "", "")
	destination := flags.String("destination", "", "")
	postgresConfig := flags.String("postgres-config", "", "")
	standardInput := flags.Bool("password-stdin", false, "")
	asJSON := flags.Bool("json", false, "")
	fail := func(exit int, code, title string) error {
		return restoreOutput(command, out, status, *asJSON, nil, exit, code, title)
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*configPath) ||
		!filepath.IsAbs(*archivePath) || !filepath.IsAbs(*destination) || filepath.Clean(*destination) != *destination ||
		!web.ValidUUID(*installation) || *release == "" {
		return fail(2, "restore.invalid_input", "Provide the protected configuration, archive, installation ID, release and replacement directory.")
	}
	info, err := os.Lstat(*configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fail(2, "restore.invalid_input", "Open a private controller configuration file.")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fail(2, "restore.invalid_input", "Open a valid controller configuration.")
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
	targetDigest, checkTarget, err := restoreTarget(cfg, *destination, *postgresConfig)
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
	validation, err := backup.ValidateRestore(ctx, archive, password, expected, checkTarget)
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
	plans := filepath.Join(cfg.InstallationDir, "restore-validations")
	info, err = os.Lstat(cfg.InstallationDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fail(1, "restore.validation_unavailable", "Use a private installation directory.")
	}
	if err := os.Mkdir(plans, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail(1, "restore.validation_unavailable", "Cannot record restore validation in the installation directory.")
	}
	plan, err := backup.RecordRestorePlan(ctx, plans, targetDigest, validation)
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

func restoreTarget(cfg config.Config, destination, postgresPath string) (string, func(context.Context) error, error) {
	checkLocal := func() error {
		parent, err := os.Lstat(filepath.Dir(destination))
		if err != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
			return backup.ErrRestoreTarget
		}
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return backup.ErrRestoreTarget
		}
		return nil
	}
	if cfg.DataProfile == "sqlite" && postgresPath == "" {
		return restoreDigest("sqlite", destination), func(context.Context) error { return checkLocal() }, nil
	}
	if cfg.DataProfile != "postgres" || postgresPath == "" {
		return "", nil, backup.ErrRestoreTarget
	}
	profile, err := readSetupPostgresProfile(postgresPath)
	if err != nil {
		return "", nil, err
	}
	application, err := url.Parse(cfg.PostgresURI)
	if err != nil {
		return "", nil, backup.ErrRestoreTarget
	}
	dex, err := url.Parse(profile.DexURI)
	if err != nil {
		return "", nil, backup.ErrRestoreTarget
	}
	// Credentials may be rotated. Bind host, database and role identity.
	digest := restoreDigest("postgres", destination, application.Redacted(), dex.Redacted())
	target := database.PostgresSetupTarget{ApplicationURI: cfg.PostgresURI, DexURI: profile.DexURI, Roots: profile.Roots}
	return digest, func(ctx context.Context) error {
		if err := checkLocal(); err != nil {
			return err
		}
		return backup.ValidatePostgresReplacement(ctx, target)
	}, nil
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
	ValidationID   string   `json:"validationId"`
	Release        string   `json:"release"`
	Profile        string   `json:"profile"`
	Schema         int64    `json:"schema"`
	KeyDisposition string   `json:"keyDisposition"`
	OwnedSchemas   []string `json:"ownedSchemas,omitempty"`
	ReviewSteps    []string `json:"reviewSteps"`
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
			statusCode := map[int]int{1: 500, 2: 400, 3: 404, 5: 503, 7: 422}[exit]
			envelope.Status = "failed"
			envelope.Problem = &web.Problem{Type: "about:blank", Status: statusCode, Code: code, Title: title}
		}
		if err := json.NewEncoder(out).Encode(envelope); err != nil {
			return cliExit{1, "Cannot write restore result."}
		}
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
