package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestWorkspaceReconciliationDescribesInventoryConflict ensures a normal
// start shows an actionable failure while preserving uncertain inventory.
func TestWorkspaceReconciliationDescribesInventoryConflict(t *testing.T) {
	err := workspaceEffectError(operation.Operation{OriginatingAction: "workspace.start"}, provider.ErrOwnershipMismatch)
	var waiting *operation.StepWaiting
	if !errors.As(err, &waiting) || !errors.Is(err, provider.ErrOwnershipMismatch) || !strings.Contains(waiting.Detail, "ownership") {
		t.Fatalf("workspace start inventory conflict = %v, want actionable ownership evidence", err)
	}
}
