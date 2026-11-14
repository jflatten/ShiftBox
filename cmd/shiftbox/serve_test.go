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

// TestControllerRuntimeRequirements rejects incomplete runtime settings before
// starting Dex or binding a listener. Error text must omit input values.
func TestControllerRuntimeRequirements(t *testing.T) {
	const fixture = "private-invalid-address"
	if err := checkControllerRuntime(config.ControllerRuntime{}); err == nil {
		t.Fatal("missing runtime settings were accepted")
	}
	settings := config.ControllerRuntime{
		BrowserAddress: ":443", GuestHost: "host.example.test",
		EnrollmentAddress: ":8443", PollAddress: ":8444", TunnelAddress: ":8445",
		UTMStorage: "/tmp/UTM Documents",
		UTMMinimum: config.VMResources{CPU: 2, MemoryMiB: 4096, DiskMiB: 32768},
		UTMMaximum: config.VMResources{CPU: 4, MemoryMiB: 8192, DiskMiB: 32768},
	}
	if err := checkControllerRuntime(settings); err != nil {
		t.Fatalf("valid controller settings rejected: %v", err)
	}
	settings.TunnelAddress = fixture
	err := checkControllerRuntime(settings)
	if err == nil || strings.Contains(err.Error(), fixture) {
		t.Fatal("invalid listener address accepted or disclosed")
	}
	settings.TunnelAddress = settings.PollAddress
	if err := checkControllerRuntime(settings); err == nil {
		t.Fatal("overlapping guest listener settings accepted")
	}
	settings.TunnelAddress = ":8445"
	settings.UTMMaximum.MemoryMiB = 1024
	if err := checkControllerRuntime(settings); err == nil {
		t.Fatal("inverted UTM resource bounds accepted")
	}
}

// TestReadControllerGitHub keeps the optional App credentials in a private
// configuration file and redacts malformed credential input.
func TestReadControllerGitHub(t *testing.T) {
	if credentials, err := readControllerGitHub(""); err != nil || credentials != nil {
		t.Fatal("an omitted GitHub App must leave the integration unconfigured")
	}
	path := filepath.Join(t.TempDir(), "github.json")
	const input = `{"clientId":"client","clientSecret":"private-fixture","installationId":123}`
	if err := os.WriteFile(path, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readControllerGitHub(path); err == nil || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("accepted public GitHub configuration or disclosed its contents")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	credentials, err := readControllerGitHub(path)
	if err != nil || credentials == nil || credentials.ClientID != "client" || credentials.InstallationID != 123 {
		t.Fatalf("private GitHub App configuration rejected: %v", err)
	}
}

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
