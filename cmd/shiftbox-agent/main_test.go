package main

import (
	"errors"
	"testing"
)

// TestAgentRejectsOtherCommands ensures the guest cannot select an arbitrary
// command or override supervisor-owned paths through arguments.
func TestAgentRejectsOtherCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"exec", "id"}, {"serve", "--state-dir", "/tmp"}} {
		if err := run(t.Context(), args); !errors.Is(err, errUsage) {
			t.Errorf("run(%v) = %v, want usage error", args, err)
		}
	}
}
