package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestMigratePreflightInvalidInput verifies the structured failure contains
// neither the manifest path nor a claim that checks passed.
func TestMigratePreflightInvalidInput(t *testing.T) {
	var out, status bytes.Buffer
	err := run(t.Context(), []string{"migrate", "preflight", "--json", "--config", "/private/setup.json", "--release", "/private/missing.json"}, &out, &status)
	var failure cliExit
	if !errors.As(err, &failure) || failure.code != 2 || !strings.Contains(out.String(), `"code":"migration.invalid_release"`) {
		t.Fatalf("migrate validation = %v, %s; want exit 2 and stable JSON error", err, &out)
	}
	if strings.Contains(out.String()+status.String(), "/private/") {
		t.Fatal("migrate preflight reported a local input path")
	}
}

// TestMigratePreflightDeniesUnverifiedInstallation checks actual command
// dispatch against a missing active data store and protects local diagnostics.
func TestMigratePreflightDeniesUnverifiedInstallation(t *testing.T) {
	dir := t.TempDir()
	manifest, configuration, missingDatabase := filepath.Join(dir, "release.json"), filepath.Join(dir, "configuration.json"), filepath.Join(dir, "active.db")
	component := release.ArtifactComponent{Version: "fixture", SHA256: strings.Repeat("a", 64)}
	docs := map[string]any{
		manifest: release.MigrationRelease{SchemaVersion: 1, ReleaseID: "fixture", ProtocolVersion: "1", Controller: component,
			Dex: component, UTM: component, PackerFork: component, Ubuntu: component, Guest: component, IDE: component},
		configuration: config.Config{InstallationDir: dir, SQLitePath: missingDatabase, DataProfile: "sqlite", ManagementOrigin: "https://management.test",
			ExternalAuthIntervalSeconds: 14400, Runtime: &config.ControllerRuntime{}},
	}
	for path, doc := range docs {
		encoded, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, status bytes.Buffer
	err := run(t.Context(), []string{"migrate", "preflight", "--json", "--config", configuration, "--release", manifest}, &out, &status)
	var exit cliExit
	var envelope struct {
		APIVersion, Command, Status string
		Result                      *release.MigrationReport
		Problem                     *web.Problem
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &envelope); decodeErr != nil || !errors.As(err, &exit) || exit.code != 7 ||
		envelope.APIVersion != "shiftbox.io/cli/v1" || envelope.Command != "migrate preflight" || envelope.Status != "failed" ||
		envelope.Result != nil || envelope.Problem == nil || envelope.Problem.Code != "migration.installation_unverified" ||
		envelope.Problem.Status != 409 || !strings.Contains(envelope.Problem.Title, "installation key") {
		t.Fatalf("migrate preflight for missing database = %v, %+v (%v), want exit 7 with installation guidance", err, envelope, decodeErr)
	}
	if strings.Contains(out.String()+status.String()+err.Error(), dir) {
		t.Error("preflight exposed the installation path")
	}
	if _, err := os.Lstat(missingDatabase); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight changed the missing database: %v", err)
	}
}

// TestMigrationOutputStableExits covers ready, prerequisite and service
// outcomes without requiring UTM, PostgreSQL, or a running controller.
func TestMigrationOutputStableExits(t *testing.T) {
	for _, test := range []struct {
		name               string
		exit, problemCode  int
		status, stableCode string
	}{
		{"ready", 0, 0, "succeeded", ""},
		{"input", 2, 400, "failed", "migration.invalid_input"},
		{"unavailable", 5, 503, "failed", "migration.guest_inventory_unavailable"},
		{"blocked", 7, 409, "failed", "migration.blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var report *release.MigrationReport
			if test.exit == 0 || test.exit == 7 {
				report = &release.MigrationReport{ReleaseID: "fixture", Ready: test.exit == 0}
			}
			if test.exit == 7 {
				report.Blockers = []release.MigrationBlocker{{Code: "migration.backup_required", Detail: "Verify a management backup."}}
			}
			var out, status bytes.Buffer
			err := migrationOutput(&out, &status, true, report, web.Problem{Code: test.stableCode, Title: "Fixture preflight failure"}, test.exit)
			var cliErr cliExit
			if test.exit == 0 && err != nil || test.exit != 0 && (!errors.As(err, &cliErr) || cliErr.code != test.exit) {
				t.Fatalf("migrationOutput(%s) = %v, want exit %d", test.name, err, test.exit)
			}
			var envelope struct {
				Status  string
				Result  *release.MigrationReport
				Problem *web.Problem
			}
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Status != test.status || status.Len() != 0 {
				t.Fatalf("migrationOutput(%s) = %+v (%v), want %s JSON on stdout", test.name, envelope, err, test.status)
			}
			if test.exit == 0 && (envelope.Problem != nil || envelope.Result == nil || !envelope.Result.Ready) || test.exit != 0 &&
				(envelope.Problem == nil || envelope.Problem.Status != test.problemCode || envelope.Problem.Code != test.stableCode) {
				t.Fatalf("migrationOutput(%s) = %+v, want problem %d/%s or ready success", test.name, envelope, test.problemCode, test.stableCode)
			}
			if test.exit == 7 {
				if envelope.Result == nil || len(envelope.Result.Blockers) != 1 || envelope.Result.Blockers[0] != report.Blockers[0] {
					t.Errorf("blocked CLI report = %+v, want %+v", envelope.Result, report)
				}
				out.Reset()
				err := migrationOutput(&out, &status, false, report, web.Problem{Code: test.stableCode, Title: "Fixture preflight failure"}, test.exit)
				if !errors.As(err, &cliErr) || cliErr.code != 7 || out.Len() != 0 ||
					!strings.Contains(status.String(), "migration.backup_required Verify a management backup.") {
					t.Errorf("text blocked preflight = %v, %q, want exit 7 and actionable stderr detail", err, status.String())
				}
			}
		})
	}
}

// TestMigrationDexRequiresPinnedExecutable checks tool identity and digest
// using a disposable executable file with no external process invocation.
func TestMigrationDexRequiresPinnedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dex")
	fixture := []byte("pinned fixture bytes")
	if err := os.WriteFile(path, fixture, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fixture)
	pinned := release.ArtifactComponent{Version: identity.DexVersion, SHA256: hex.EncodeToString(digest[:])}
	if !migrationDexMatches(path, pinned) {
		t.Fatal("migrationDexMatches(pinned executable) = false, want true")
	}
	for _, input := range []release.ArtifactComponent{
		{Version: "unreviewed", SHA256: pinned.SHA256},
		{Version: pinned.Version, SHA256: strings.Repeat("0", 64)},
	} {
		if migrationDexMatches(path, input) {
			t.Errorf("migrationDexMatches(%+v) = true, want refusal", input)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if migrationDexMatches(path, pinned) {
		t.Error("migrationDexMatches(non-executable fixture) = true, want refusal")
	}
}

// TestMigrationStorageRequiresBothDirectories checks that a writable
// installation directory cannot mask unavailable workspace storage.
func TestMigrationStorageRequiresBothDirectories(t *testing.T) {
	installation := t.TempDir()
	workspaceStorage := t.TempDir()
	if !migrationStorageWritable(installation, workspaceStorage) {
		t.Error("migrationStorageWritable rejected both existing writable directories")
	}
	if migrationStorageWritable(installation, filepath.Join(workspaceStorage, "missing")) {
		t.Error("migrationStorageWritable accepted a missing workspace directory")
	}
	for _, dir := range []string{installation, workspaceStorage} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("migrationStorageWritable left %d entries in a probe directory (%v)", len(entries), err)
		}
	}
}
