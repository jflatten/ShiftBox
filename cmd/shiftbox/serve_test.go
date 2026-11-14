package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
)

// TestServeRequiresActiveDatabase verifies that the CLI fails before
// starting any listener or creating an installation.
func TestServeRequiresActiveDatabase(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "bootstrap.db")
	configPath := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(config.Config{InstallationDir: dir, SQLitePath: databasePath,
		DataProfile: "sqlite", ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out, status bytes.Buffer
	err = run(t.Context(), []string{"serve", "--config", configPath}, &out, &status)
	if !errors.Is(err, app.ErrActiveDatabase) || out.Len() != 0 || status.Len() != 0 {
		t.Fatalf("serve for missing database = %v, want preflight error and no output", err)
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("serve created database: %v", err)
	}
}

// TestServeDoesNotEchoSecretArguments keeps unexpected configuration or
// credential arguments out of command diagnostics.
func TestServeDoesNotEchoSecretArguments(t *testing.T) {
	var out, status bytes.Buffer
	err := run(t.Context(), []string{"serve", "--password=sentinel-secret"}, &out, &status)
	if !errors.Is(err, errServeUsage) || strings.Contains(err.Error()+out.String()+status.String(), "sentinel-secret") {
		t.Fatal("serve accepted or echoed an unrecognized credential argument")
	}
}
