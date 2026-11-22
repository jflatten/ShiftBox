package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestGuestUpdateUsesPersistentInstallation loads a pinned artifact from the
// installation even when cloud configuration is generated in another temporary directory.
func TestGuestUpdateUsesPersistentInstallation(t *testing.T) {
	installation := t.TempDir()
	transient := t.TempDir()
	if err := os.WriteFile(filepath.Join(transient, "config.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(installation, "releases", "development")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("pinned guest fixture")
	digest := sha256.Sum256(binary)
	pin := release.ArtifactComponent{Version: "fixture", SHA256: hex.EncodeToString(digest[:])}
	manifest := release.MigrationRelease{SchemaVersion: 1, ReleaseID: "development", ProtocolVersion: "1", Controller: pin, Dex: pin, UTM: pin, PackerFork: pin, Ubuntu: pin, IDE: pin,
		Guest: release.ArtifactComponent{Version: "development", SHA256: hex.EncodeToString(digest[:])}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"release.json": encoded, "shiftbox-agent": binary, "shiftbox-agent.sig": make([]byte, 64)} {
		if err := os.WriteFile(filepath.Join(folder, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	load := controllerGuestUpdateLoader(config.Config{InstallationDir: installation}, "1")
	got, err := load(t.Context(), "development")
	if err != nil || got.Metadata.Digest != digest || string(got.Binary) != string(binary) {
		t.Fatalf("persistent guest artifact=%+v (%v), want pinned installation artifact", got.Metadata, err)
	}
	if _, err := load(t.Context(), "unpublished"); err == nil {
		t.Error("unpublished guest artifact accepted")
	}
}
