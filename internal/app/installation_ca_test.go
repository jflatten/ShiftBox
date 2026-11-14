package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

// TestInstallationCARetainsTrust verifies encrypted persistence, restart
// stability, scope binding, and refusal to replace damaged authority.
func TestInstallationCARetainsTrust(t *testing.T) {
	directory := t.TempDir()
	now := time.Now().UTC()
	cipher, err := cryptostore.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	const installation = "installation-id"
	if _, err := LoadInstallationCA(directory, installation, cipher, now); err == nil {
		t.Fatal("load silently created missing CA")
	}
	first, err := EnsureInstallationCA(directory, installation, cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	next, err := EnsureInstallationCA(directory, installation, cipher, now.Add(time.Hour))
	if err != nil || !bytes.Equal(first.Certificate.Raw, next.Certificate.Raw) {
		t.Fatalf("setup retry changed authority: %v", err)
	}
	path := filepath.Join(directory, "guest-ca.json")
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("PRIVATE KEY")) {
		t.Fatalf("CA storage failed or exposed a plaintext key: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("CA storage did not use private permissions: %v", err)
	}
	if _, err := LoadInstallationCA(directory, "other", cipher, now); err == nil {
		t.Error("loaded another installation's key")
	}
	if _, err := LoadInstallationCA(directory, installation, cipher, first.Certificate.NotAfter); err == nil {
		t.Error("loaded expired CA")
	}
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureInstallationCA(directory, installation, cipher, now); err == nil {
		t.Error("setup replaced damaged CA")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "damaged" {
		t.Errorf("damaged authority changed: %v", err)
	}
}
