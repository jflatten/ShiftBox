package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSetupPostgresProfile verifies that local setup accepts scoped
// configuration from a private file and redacts malformed content.
func TestReadSetupPostgresProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")
	const raw = `{"dexUri":"postgres://dex:fixture-secret@db.test/shiftbox?sslmode=verify-full"}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := readSetupPostgresProfile(path)
	if err != nil || profile == nil || !strings.Contains(profile.DexURI, "fixture-secret") || profile.Roots == nil {
		t.Fatalf("private identity config was not loaded, error %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupPostgresProfile(path); err == nil {
		t.Fatal("read an identity config with broad permissions")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw+"fixture-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupPostgresProfile(path); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("malformed identity config returned %v, want redacted error", err)
	}
}
