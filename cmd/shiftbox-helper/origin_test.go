package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPortableHelperRemembersController verifies that a portable app prompts
// for an HTTPS origin once and reuses it when the login item starts again.
func TestPortableHelperRemembersController(t *testing.T) {
	executable := helperOriginFixture(t, `{"portable":true}`)
	data := t.TempDir()
	calls := 0
	prompt := func(context.Context) (string, error) {
		calls++
		return "https://shiftbox.example.test:8443/", nil
	}
	for range 2 {
		origin, err := helperOrigin(t.Context(), "", executable, data, prompt)
		if err != nil || origin != "https://shiftbox.example.test:8443" {
			t.Fatalf("portable helper origin = %q (%v), want canonical HTTPS origin", origin, err)
		}
	}
	if calls != 1 {
		t.Fatalf("portable origin prompted %d times, want 1", calls)
	}
}

// TestPackagedControllerOriginTakesPrecedence keeps a private installation's
// bundled URL independent of an earlier portable choice on the same Mac.
func TestPackagedControllerOriginTakesPrecedence(t *testing.T) {
	executable := helperOriginFixture(t, `{"origin":"https://shiftbox.rc3.test:8443"}`)
	origin, err := helperOrigin(t.Context(), "", executable, t.TempDir(), func(context.Context) (string, error) {
		t.Fatal("an installation-specific package asked for an origin")
		return "", nil
	})
	if err != nil || origin != "https://shiftbox.rc3.test:8443" {
		t.Fatalf("packaged helper origin = %q (%v), want rc3 HTTPS origin", origin, err)
	}
}

func helperOriginFixture(t *testing.T, configuration string) string {
	t.Helper()
	contents := t.TempDir()
	resources := filepath.Join(contents, "Resources")
	for _, dir := range []string{resources, filepath.Join(contents, "MacOS")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(resources, "shiftbox-helper.json"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(contents, "MacOS", "shiftbox-helper")
}
