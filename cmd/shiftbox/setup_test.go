package main

import (
	"os"
	"testing"
)

// TestControllerReleaseMatchesBuild checks that setup uses the expected guest
// compatibility identity in both default and release-stamped controller builds.
func TestControllerReleaseMatchesBuild(t *testing.T) {
	expected := os.Getenv("SHIFTBOX_EXPECT_CONTROLLER_RELEASE")
	if expected == "" {
		expected = "development"
	}
	if controllerRelease != expected {
		t.Errorf("controllerRelease = %q, want build compatibility identity %q", controllerRelease, expected)
	}
}
