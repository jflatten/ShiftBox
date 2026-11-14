package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
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
