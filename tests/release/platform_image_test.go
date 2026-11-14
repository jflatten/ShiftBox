package release_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/release"
)

const (
	configPath = "ubuntu.utm/config.plist"
	diskPath   = "ubuntu.utm/Data/platform.qcow2"
)

// TestValidatePlatformArtifact accepts a complete versioned amd64 export
// whose configuration and disk match the manifest digests.
func TestValidatePlatformArtifact(t *testing.T) {
	root, manifest := artifactFixture(t)
	artifact, err := release.ValidateArtifact(context.Background(), root, bytes.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Manifest.ReleaseID != "ubuntu-reference-1" || artifact.Bundle != "ubuntu.utm" {
		t.Fatalf("ValidateArtifact = %+v, want reference release and bundle", artifact)
	}
	if artifact.ManifestSHA256 != fmt.Sprintf("%x", sha256.Sum256(manifest)) {
		t.Errorf("manifest digest = %s, want SHA-256 of the imported document", artifact.ManifestSHA256)
	}
}

// TestStagePlatformArtifact creates a separate bundle under the requested
// workspace name and leaves the source content and path intact.
func TestStagePlatformArtifact(t *testing.T) {
	root, manifest := artifactFixture(t)
	stageRoot := t.TempDir()
	const name = "ShiftBox-workspace-1.utm"
	artifact, err := release.StageArtifact(context.Background(), root, bytes.NewReader(manifest), stageRoot, name)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Bundle != filepath.Join(stageRoot, name) {
		t.Fatalf("staged bundle = %q, want workspace bundle in staging directory", artifact.Bundle)
	}
	if err := os.WriteFile(filepath.Join(artifact.Bundle, "Data", "platform.qcow2"), []byte("workspace copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := release.ValidateArtifact(context.Background(), root, bytes.NewReader(manifest)); err != nil {
		t.Errorf("source validation after editing workspace copy = %v", err)
	}
	if _, err := release.StageArtifact(context.Background(), root, bytes.NewReader(manifest), stageRoot, name); err == nil {
		t.Error("staging overwrote an existing workspace bundle")
	}
}

// TestStagePlatformArtifactRejectsCorruption checks that a failed import
// leaves no staging bundle for UTM to pick up.
func TestStagePlatformArtifactRejectsCorruption(t *testing.T) {
	root, manifest := artifactFixture(t)
	writeArtifactFile(t, root, diskPath, []byte("corruption"))
	stageRoot := t.TempDir()
	const name = "ShiftBox-workspace-1.utm"
	if _, err := release.StageArtifact(context.Background(), root, bytes.NewReader(manifest), stageRoot, name); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Errorf("StageArtifact with corrupt disk = %v, want invalid artifact", err)
	}
	if _, err := os.Stat(filepath.Join(stageRoot, name)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging path after failed import = %v, want absent", err)
	}
}

// TestValidatePlatformArtifactRejectsChangedFiles rejects an altered disk,
// an unlisted drive, and a symlink that resolves outside the export.
func TestValidatePlatformArtifactRejectsChangedFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*testing.T, string)
	}{
		{name: "corrupt disk", alter: func(t *testing.T, root string) {
			writeArtifactFile(t, root, diskPath, []byte("different image"))
		}},
		{name: "unlisted disk", alter: func(t *testing.T, root string) {
			writeArtifactFile(t, root, "ubuntu.utm/Data/extra.qcow2", []byte("unlisted"))
		}},
		{name: "symlink", alter: func(t *testing.T, root string) {
			outside := t.TempDir()
			writeArtifactFile(t, outside, "image", []byte("reference disk"))
			path := filepath.Join(root, diskPath)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "image"), path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, manifest := artifactFixture(t)
			tc.alter(t, root)
			if _, err := release.ValidateArtifact(context.Background(), root, bytes.NewReader(manifest)); !errors.Is(err, release.ErrInvalidArtifact) {
				t.Errorf("ValidateArtifact with %s = %v, want invalid artifact", tc.name, err)
			}
		})
	}
}

// TestValidatePlatformArtifactRejectsInvalidManifest checks the MVP
// architecture, provenance, manifest shape, and resource-path boundaries.
func TestValidatePlatformArtifactRejectsInvalidManifest(t *testing.T) {
	root, manifest := artifactFixture(t)
	for _, tc := range []struct {
		name string
		from string
		to   string
	}{
		{name: "architecture", from: "amd64", to: "arm64"},
		{name: "missing provenance revision", from: "release-commit", to: ""},
		{name: "unrecognized field", from: "\"architecture\"", to: "\"platformArch\""},
		{name: "outside bundle", from: diskPath, to: "../reference.qcow2"},
		{name: "duplicate path", from: diskPath, to: configPath},
		{name: "second bundle", from: diskPath, to: "other.utm/Data/platform.qcow2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := bytes.Replace(manifest, []byte(tc.from), []byte(tc.to), 1)
			if bytes.Equal(input, manifest) {
				t.Fatal("fixture did not have the expected field")
			}
			if _, err := release.ValidateArtifact(context.Background(), root, bytes.NewReader(input)); !errors.Is(err, release.ErrInvalidArtifact) {
				t.Errorf("ValidateArtifact with %s = %v, want invalid artifact", tc.name, err)
			}
		})
	}
}

func artifactFixture(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	config := []byte("reference VM configuration")
	disk := []byte("reference disk")
	writeArtifactFile(t, root, configPath, config)
	writeArtifactFile(t, root, diskPath, disk)
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"releaseId":     "ubuntu-reference-1", "architecture": "amd64", "platformVersion": "24.04",
		"protocolVersion": "1",
		"guest":           map[string]string{"version": "1", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("guest")))},
		"ide":             map[string]string{"version": "1", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("ide")))},
		"provenance":      map[string]string{"source": "https://example.com/platform", "revision": "release-commit"},
		"resources": []map[string]string{
			{"path": configPath, "type": "vm", "sha256": fmt.Sprintf("%x", sha256.Sum256(config))},
			{"path": diskPath, "type": "disk", "sha256": fmt.Sprintf("%x", sha256.Sum256(disk))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

func writeArtifactFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestGeneratePlatformManifest verifies an export manifest hashes the actual
// generated files and refuses to declare unexpected files as owned resources.
func TestGeneratePlatformManifest(t *testing.T) {
	root, raw := artifactFixture(t)
	const firmware = "ubuntu.utm/Data/efi_vars.fd"
	writeArtifactFile(t, root, firmware, []byte("fixture firmware"))
	var metadata release.ArtifactManifest
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.Resources = nil
	manifest, err := release.GenerateArtifactManifest(t.Context(), root, "ubuntu.utm", metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Resources) != 3 || manifest.Resources[0].Path != firmware || manifest.Resources[0].Type != "media" {
		t.Fatalf("resources = %+v, want ordered firmware, disk, and VM records", manifest.Resources)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.ValidateArtifact(t.Context(), root, bytes.NewReader(encoded)); err != nil {
		t.Fatalf("validating generated manifest: %v", err)
	}
	writeArtifactFile(t, root, "ubuntu.utm/Data/extra.key", []byte("fixture"))
	if _, err := release.GenerateArtifactManifest(t.Context(), root, "ubuntu.utm", metadata); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Fatalf("manifest with unexpected file = %v, want invalid artifact", err)
	}
	if _, err := release.GenerateArtifactManifest(t.Context(), root, "../ubuntu.utm", metadata); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Fatalf("manifest with nested export path = %v, want invalid artifact", err)
	}
}
