package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestReadLocalGuestUpdate checks that only bytes matching the pinned release
// and protocol are available and that a linked artifact is rejected.
func TestReadLocalGuestUpdate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "releases")
	folder := filepath.Join(root, "fixture")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("fixture guest executable")
	digest := sha256.Sum256(body)
	component := ArtifactComponent{Version: "fixture", SHA256: hex.EncodeToString(digest[:])}
	manifest := MigrationRelease{SchemaVersion: 1, ReleaseID: "fixture", ProtocolVersion: "1", Guest: component,
		Controller: component, Dex: component, UTM: component, PackerFork: component, Ubuntu: component, IDE: component}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := GuestUpdateMetadata{Release: "fixture", Protocol: "1", Size: int64(len(body)), Digest: digest}
	signature := ed25519.Sign(private, metadata.SignedBytes())
	for name, content := range map[string][]byte{"release.json": encoded, "shiftbox-agent": body, "shiftbox-agent.sig": signature} {
		if err := os.WriteFile(filepath.Join(folder, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadLocalGuestUpdate(t.Context(), root, "fixture", "1")
	if err != nil || got.Metadata != metadata || !bytes.Equal(got.Binary, body) ||
		!ed25519.Verify(public, got.Metadata.SignedBytes(), got.Signature) {
		t.Fatalf("ReadLocalGuestUpdate(valid) = %+v (%v), want pinned artifact", got.Metadata, err)
	}
	if _, err := ReadLocalGuestUpdate(t.Context(), root, "fixture", "2"); !errors.Is(err, ErrGuestUpdateUnavailable) {
		t.Errorf("ReadLocalGuestUpdate(wrong protocol) = %v, want unavailable", err)
	}
	path := filepath.Join(folder, "shiftbox-agent")
	if err := os.WriteFile(path, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocalGuestUpdate(t.Context(), root, "fixture", "1"); !errors.Is(err, ErrGuestUpdateUnavailable) {
		t.Errorf("ReadLocalGuestUpdate(changed binary) = %v, want unavailable", err)
	}
	copy := filepath.Join(folder, "agent-copy")
	if err := os.WriteFile(copy, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(copy, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLocalGuestUpdate(t.Context(), root, "fixture", "1"); !errors.Is(err, ErrGuestUpdateUnavailable) {
		t.Errorf("ReadLocalGuestUpdate(symlink) = %v, want unavailable", err)
	}
}

// TestSignLocalGuestUpdate ensures that signing binds the pinned binary and
// leaves an existing signature intact when the binary no longer matches.
func TestSignLocalGuestUpdate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "releases")
	folder := filepath.Join(root, "candidate")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("fixture release agent")
	digest := sha256.Sum256(body)
	pin := ArtifactComponent{Version: "candidate", SHA256: hex.EncodeToString(digest[:])}
	manifest := MigrationRelease{SchemaVersion: 1, ReleaseID: pin.Version, ProtocolVersion: "1",
		Controller: pin, Dex: pin, UTM: pin, PackerFork: pin, Ubuntu: pin, Guest: pin, IDE: pin}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"release.json": raw, "shiftbox-agent": body} {
		if err := os.WriteFile(filepath.Join(folder, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignLocalGuestUpdate(t.Context(), root, pin.Version, private); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLocalGuestUpdate(t.Context(), root, pin.Version, "1")
	if err != nil || !ed25519.Verify(public, got.Metadata.SignedBytes(), got.Signature) {
		t.Fatalf("signed local artifact = %v, want verifiable publisher signature", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "shiftbox-agent"), []byte("unexpected binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SignLocalGuestUpdate(t.Context(), root, pin.Version, private); !errors.Is(err, ErrGuestUpdateUnavailable) {
		t.Errorf("signing a changed binary = %v, want unavailable", err)
	}
	if sig, err := os.ReadFile(filepath.Join(folder, "shiftbox-agent.sig")); err != nil || !bytes.Equal(sig, got.Signature) {
		t.Error("failed signing changed the published signature")
	}
}
