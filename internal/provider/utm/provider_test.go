package utm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestProviderAdvertisesCapabilities verifies that successful inspection
// advertises AMD64 and, on Apple Silicon, hardware-virtualized ARM64.
func TestProviderAdvertisesCapabilities(t *testing.T) {
	minimum := provider.Resources{CPU: 1, MemoryBytes: 512 << 20, DiskBytes: 64 << 20}
	maximum := provider.Resources{CPU: 4, MemoryBytes: 4096 << 20, DiskBytes: 32768 << 20}
	p := &Provider{minimum: minimum, maximum: maximum, controller: &PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
	}}
	got, err := p.Capabilities(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	architectures := []string{"amd64"}
	if runtime.GOARCH == "arm64" {
		architectures = append(architectures, "arm64")
	}
	want := provider.Capabilities{
		Architectures: architectures,
		Name:          "utm", Version: "5.0.8", Architecture: "amd64", ExecutionMode: "emulated",
		Evidence: "UTM 5.0.8 native QEMU scripting",
		Create:   true, Start: true, Stop: true, Delete: true, NetworkStoppedOnly: true,
		NetworkModes: []string{"shared"}, RecoveryMechanisms: []string{"media"}, Minimum: minimum, Maximum: maximum,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Capabilities after verified inspection mismatch (-want +got):\n%s", diff)
	}
}

// TestProviderRequiresVerifiedInspection keeps capabilities unavailable when
// native inspection cannot prove absence for the capability probe.
func TestProviderRequiresVerifiedInspection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inspectErr error
		want       error
	}{
		{name: "unexpected workspace", want: provider.ErrOutcomeUnknown},
		{name: "permission denied", inspectErr: provider.ErrPermission, want: provider.ErrPermission},
		{name: "unsupported build", inspectErr: provider.ErrCapabilityUnsupported, want: provider.ErrCapabilityUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Provider{controller: &PowerController{
				inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
					return provider.Inventory{}, tc.inspectErr
				},
			}}
			got, err := p.Capabilities(t.Context())
			if !errors.Is(err, tc.want) || !cmp.Equal(got, provider.Capabilities{}) {
				t.Errorf("Capabilities with %s = %+v, %v, want empty capabilities and %v", tc.name, got, err, tc.want)
			}
		})
	}
}

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
