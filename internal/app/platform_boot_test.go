package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/release"
)

type bootTestProvider struct{ *fake.Provider }

func (p bootTestProvider) ImportCandidate(ctx context.Context, req provider.CreateRequest, _ release.Artifact) (provider.Inventory, error) {
	return p.Create(ctx, req)
}

// TestPlatformBootCleansUpLostImport checks that an uncertain import never
// publishes the candidate and that its created clone is removed by ownership.
func TestPlatformBootCleansUpLostImport(t *testing.T) {
	machines := bootTestProvider{fake.New()}
	machines.LoseReply("create")
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	req := provider.CreateRequest{Ref: ref, OperationID: uuid.NewString(), ArtifactID: uuid.NewString(), Release: "test", NetworkMode: "nat",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 1024 << 20, DiskBytes: 32768 << 20}}
	artifact := release.Artifact{Manifest: release.ArtifactManifest{Guest: release.ArtifactComponent{Version: "test"}, ProtocolVersion: "1"}}
	err := ValidatePlatformBoot(t.Context(), t.TempDir(), machines, req, artifact, GuestListenerConfig{
		PublicHost: "127.0.0.1", EnrollmentAddress: "127.0.0.1:0", PollAddress: "127.0.0.1:0",
	})
	if !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("validation after lost create reply = %v, want uncertain outcome", err)
	}
	vm, err := machines.Inspect(t.Context(), ref)
	if err != nil || vm.Power != provider.Deleted || len(vm.Resources) != 1 || !vm.Resources[0].Shared {
		t.Fatalf("inspection after failed validation = %+v, %v, want only the shared catalog resource", vm, err)
	}
}
