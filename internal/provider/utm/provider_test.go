package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestProviderPreservesOrphan verifies that native deregistration alone
// cannot turn an existing managed bundle into a successful absence.
func TestProviderPreservesOrphan(t *testing.T) {
	ref := ownedDeleteRequest().Ref
	storage := t.TempDir()
	folder := filepath.Join(storage, managedBundleName(ref))
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	p := &Provider{storage: storage, controller: &PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
	}}
	got, err := p.Inspect(context.Background(), ref)
	if !errors.Is(err, provider.ErrPartial) || len(got.Resources) != 1 {
		t.Fatalf("Inspect orphan = %+v, %v, want remaining bundle", got, err)
	}
}

// TestProviderRejectsAmbiguousRecoveryMedia keeps a stopped VM untouched
// when the adapter cannot identify exactly one owned removable drive.
func TestProviderRejectsAmbiguousRecoveryMedia(t *testing.T) {
	ref := ownedDeleteRequest().Ref
	inventory := provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Stopped}
	for _, id := range []string{"vm/one", "vm/two"} {
		inventory.Resources = append(inventory.Resources, provider.OwnedResource{ID: id, Type: "media",
			Generation: ref.Generation, OwnershipMarker: ref.OwnershipMarker})
	}
	p := &Provider{controller: &PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return inventory, nil
		},
	}}
	req := provider.EnrollmentRequest{Ref: ref, Eligible: true, OperationID: "recovery"}
	if _, err := p.DeliverEnrollment(context.Background(), req); !errors.Is(err, provider.ErrInvalidState) {
		t.Fatalf("DeliverEnrollment with two drives = %v, want invalid state", err)
	}
}

// TestProviderHidesUnverifiedNetworkPolicies confirms that requesting a
// restricted mode cannot reach UTM as a best-effort update.
func TestProviderHidesUnverifiedNetworkPolicies(t *testing.T) {
	p := &Provider{}
	if _, err := p.ApplyNetwork(context.Background(), provider.NetworkRequest{Mode: "isolated"}); !errors.Is(err, provider.ErrCapabilityUnsupported) {
		t.Fatalf("ApplyNetwork(isolated) = %v, want unsupported", err)
	}
}
