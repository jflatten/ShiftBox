package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadLocalMigrationRelease checks that the API's release ID selects a
// private, matching manifest and cannot substitute another path or release.
func TestReadLocalMigrationRelease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "releases")
	folder := filepath.Join(root, "v1.2.3")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	pin := ArtifactComponent{Version: "fixture", SHA256: strings.Repeat("a", 64)}
	doc := MigrationRelease{SchemaVersion: 1, ReleaseID: "v1.2.3", ProtocolVersion: "v1", Controller: pin,
		Dex: pin, UTM: pin, PackerFork: pin, Ubuntu: pin, Guest: pin, IDE: pin}
	path := filepath.Join(folder, "release.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if got, err := ReadLocalMigrationRelease(root, "v1.2.3"); err != nil || got != doc {
		t.Fatalf("ReadLocalMigrationRelease = %+v, %v; want matching manifest", got, err)
	}
	if _, err := ReadLocalMigrationRelease(root, "../releases/v1.2.3"); err == nil {
		t.Fatal("accepted a release ID with path components")
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocalMigrationRelease(root, "v1.2.3"); err == nil {
		t.Fatal("accepted a publicly writable manifest")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	doc.ReleaseID = "replacement"
	write()
	if _, err := ReadLocalMigrationRelease(root, "v1.2.3"); err == nil {
		t.Fatal("accepted a manifest for a different release")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	doc.ReleaseID = "v1.2.3"
	write()
	if err := os.Rename(path, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocalMigrationRelease(root, "v1.2.3"); err == nil {
		t.Fatal("accepted a redirected manifest")
	}
}
