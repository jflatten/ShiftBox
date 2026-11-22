package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// DeleteManagedVM deletes a stopped VM and confirms that the bundle recorded
// at provisioning is absent. The path must come from trusted provisioning
// state. External recovery images are tracked and cleaned up separately.
func (p *PowerController) DeleteManagedVM(ctx context.Context, req provider.DeleteRequest, bundlePath string) (provider.Inventory, error) {
	ref := req.Ref
	if ref.InstanceID == "" || ref.InstallationID == "" || ref.WorkspaceID == "" ||
		ref.Generation < 1 || ref.OwnershipMarker == "" || req.OperationID == "" ||
		!filepath.IsAbs(bundlePath) || filepath.Ext(bundlePath) != ".utm" {
		return provider.Inventory{}, provider.ErrInvalidState
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	before, err := p.inspect(ctx, ref)
	if errors.Is(err, provider.ErrNotFound) {
		return observeDeletedBundle(ref, bundlePath)
	}
	if err != nil {
		return before, err
	}
	if before.Power != provider.Stopped {
		return before, provider.ErrInvalidState
	}
	info, err := os.Lstat(bundlePath)
	if err != nil || !info.IsDir() {
		return before, provider.ErrOutcomeUnknown
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}

	effectErr := p.apply(ctx, ref, "delete")
	after, inspectErr := p.inspect(ctx, ref)
	if effectErr != nil && !errors.Is(effectErr, provider.ErrOutcomeUnknown) {
		return after, effectErr
	}
	if errors.Is(inspectErr, provider.ErrNotFound) {
		return observeDeletedBundle(ref, bundlePath)
	}
	if inspectErr != nil {
		return before, errors.Join(provider.ErrOutcomeUnknown, inspectErr)
	}
	return after, provider.ErrOutcomeUnknown
}

func observeDeletedBundle(ref provider.WorkspaceRef, path string) (provider.Inventory, error) {
	inventory := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Unknown, ObservedAt: time.Now().UTC()}
	_, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A missing or inaccessible parent does not establish absence at
		// the managed storage location.
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return inventory, provider.ErrOutcomeUnknown
		}
		inventory.Power = provider.Deleted
		return inventory, nil
	case err != nil:
		return inventory, provider.ErrOutcomeUnknown
	default:
		inventory.Resources = []provider.OwnedResource{{
			ID: ref.InstanceID + "/bundle", Type: "bundle",
			OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation,
		}}
		return inventory, provider.ErrPartial
	}
}
