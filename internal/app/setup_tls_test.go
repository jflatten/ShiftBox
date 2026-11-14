package app_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
)

// TestBootstrapCertificateSurvivesRestart preserves the local trust decision and limits the generated identity to loopback addresses.
func TestBootstrapCertificateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-tls.pem")
	now := time.Now()
	first, err := app.EnsureBootstrapCertificate(path, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.EnsureBootstrapCertificate(path, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("restart replaced explicitly trusted bootstrap certificate")
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if err := first.Leaf.VerifyHostname(host); err != nil {
			t.Errorf("bootstrap certificate lacks %s", host)
		}
	}
	if err := first.Leaf.VerifyHostname("management.example.test"); err == nil {
		t.Fatal("bootstrap certificate covers a nonlocal management name")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap key permissions = %o, want 600", info.Mode().Perm())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.EnsureBootstrapCertificate(path, now.Add(48*time.Hour)); err == nil {
		t.Fatal("expired bootstrap certificate accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("expired bootstrap identity silently regenerated")
	}
}

// TestBootstrapCertificateRefusesUnsafeFiles rejects unreadable/malformed identity material instead of regenerating it or exposing a key.
func TestBootstrapCertificateRefusesUnsafeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-tls.pem")
	if err := os.WriteFile(path, []byte("damaged identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.EnsureBootstrapCertificate(path, time.Now()); err == nil {
		t.Fatal("malformed identity was replaced or accepted")
	}
	if _, err := app.EnsureBootstrapCertificate("relative.pem", time.Now()); err == nil {
		t.Fatal("relative identity path accepted")
	}
	good := filepath.Join(t.TempDir(), "bootstrap-tls.pem")
	if _, err := app.EnsureBootstrapCertificate(good, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(good, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.EnsureBootstrapCertificate(good, time.Now()); err == nil {
		t.Fatal("publicly readable private key accepted")
	}
}
