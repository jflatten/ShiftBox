//go:build openapi

package contract_test

import (
	"os/exec"
	"testing"
)

// TestOpenAPITypesMatchContract catches stale generated types after management API changes.
func TestOpenAPITypesMatchContract(t *testing.T) {
	check := exec.CommandContext(t.Context(), "sh", "../../scripts/check-openapi.sh")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("check-openapi.sh exited with %v, want a successful drift check:\n%s", err, output)
	}
}
