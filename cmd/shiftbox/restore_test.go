package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestRestorePasswordInput keeps password failures out of CLI diagnostics.
func TestRestorePasswordInput(t *testing.T) {
	const secret = "private fixture passphrase"
	for _, tc := range []struct {
		input          string
		confirm, valid bool
	}{
		{secret + "\n", false, true},
		{secret + "\n" + secret + "\n", true, true},
		{secret + "\nother passphrase\n", true, false},
		{"\n", false, false},
		{strings.Repeat("x", 1025) + "\n", false, false},
	} {
		got, err := readRecoveryPassphrase(strings.NewReader(tc.input), &bytes.Buffer{}, true, tc.confirm)
		if tc.valid && (err != nil || got != secret) {
			t.Fatal("valid password input rejected")
		}
		if !tc.valid && (got != "" || err == nil || strings.Contains(err.Error(), secret)) {
			t.Fatal("invalid password input did not fail safely")
		}
	}
}

// TestRestoreValidateInvalidInput checks the public CLI failure envelope.
func TestRestoreValidateInvalidInput(t *testing.T) {
	var result, status bytes.Buffer
	err := runRestore(t.Context(), []string{"validate", "--json", "--archive", "/private/archive.age"},
		strings.NewReader("unused"), &result, &status)
	var exit cliExit
	if !errors.As(err, &exit) || exit.code != 2 || !strings.Contains(result.String(), `"code":"restore.invalid_input"`) ||
		strings.Contains(result.String()+status.String(), "/private/") {
		t.Fatalf("invalid restore = %v, %s; want safe exit 2", err, &result)
	}
}

// TestRestoreValidateRecordsPlan verifies the CLI can validate a same-host
// archive even when the original application database is missing.
func TestRestoreValidateRecordsPlan(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires the supported controller Mac")
	}
	ctx := t.Context()
	host, err := app.BackupHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	cfg := config.Config{InstallationDir: dir, DataProfile: "sqlite",
		SQLitePath: filepath.Join(dir, "missing.db"), ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	path := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "installation.key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(dir, "fixture")
	if err := os.WriteFile(dump, []byte("fixture content"), 0600); err != nil {
		t.Fatal(err)
	}
	schema, err := database.CurrentSchemaVersion("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "backup.age")
	const password = "private fixture passphrase"
	_, err = backup.CreateArchive(ctx, archive, password, backup.ArchiveMetadata{
		InstallationID: id, Profile: "sqlite", Release: "development", Schema: schema, HostID: host,
	}, map[string]string{"configuration.json": path, "installation.key": key, "application.db": dump, "dex.db": dump, "guest-ca.json": dump})
	if err != nil {
		t.Fatal(err)
	}
	var out, status bytes.Buffer
	destination := filepath.Join(dir, "replacement")
	err = runRestore(ctx, []string{"validate", "--json", "--password-stdin", "--archive", archive,
		"--config", path, "--installation-id", id, "--release", "development", "--destination", destination},
		strings.NewReader(password+"\n"), &out, &status)
	if err != nil {
		t.Fatalf("restore validate = %v, %s", err, out.String())
	}
	var result struct {
		Result restoreCLIResult `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	plan, err := backup.LoadRestorePlan(filepath.Join(dir, "restore-validations"), result.Result.ValidationID)
	if err != nil || plan.Validation.KeyDisposition != "retain_matching_key" {
		t.Fatalf("restore validation result = %v, %v; want durable plan and retained key", result.Result, err)
	}
	for _, absent := range []string{destination, cfg.SQLitePath} {
		if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("validation created a database or replacement: %v", err)
		}
	}
}
