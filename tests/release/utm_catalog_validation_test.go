//go:build utm_release

package release_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/testsupport"
)

// TestUTMCatalogValidation checks native boot before publication, then verifies
// that two installations can create and independently delete owned copies.
func TestUTMCatalogValidation(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_MUTATIONS") != "5.0.8" {
		t.Skip("set SHIFTBOX_TEST_UTM_MUTATIONS=5.0.8 to enable native catalog validation")
	}
	bundle, storage, host := os.Getenv("SHIFTBOX_TEST_UTM_BUNDLE"), os.Getenv("SHIFTBOX_TEST_UTM_STORAGE"), os.Getenv("SHIFTBOX_TEST_UTM_GUEST_HOST")
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(storage) || host == "" {
		t.Fatal("set the bundle, UTM storage, and guest-reachable host")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	db := openCatalogDatabase(t, "sqlite")
	store := release.NewStore(db)
	dir := t.TempDir()
	catalog := release.NewCatalog(store, filepath.Join(dir, "catalog"))
	entry, err := catalog.ImportLocal(ctx, testsupport.ID(1), time.Now(), filepath.Dir(bundle))
	if err != nil {
		t.Fatal(err)
	}
	minimum := provider.Resources{CPU: 2, MemoryBytes: 4096 << 20, DiskBytes: 8192 << 20}
	maximum := provider.Resources{CPU: 2, MemoryBytes: 4096 << 20, DiskBytes: 32768 << 20}
	machines, err := utm.NewProvider(catalog, dir, storage, minimum, maximum)
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	bind := net.JoinHostPort(host, "0")
	check := func(ctx context.Context, artifact release.Artifact) error {
		return app.ValidatePlatformBoot(ctx, dir, machines, provider.CreateRequest{Ref: ref, OperationID: uuid.NewString(),
			ArtifactID: entry.ID, Release: artifact.Manifest.ReleaseID, NetworkMode: "shared", Resources: minimum}, artifact,
			app.GuestListenerConfig{EnrollmentAddress: bind, PollAddress: bind, PublicHost: host})
	}
	if err := catalog.ValidateImport(ctx, entry.ID, check); err != nil {
		t.Fatal(err)
	}
	if _, err := machines.Inspect(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("validation VM inventory = %v, want confirmed absence", err)
	}
	entry, err = store.Get(ctx, entry.ID)
	if err != nil || entry.State != "available" {
		t.Fatalf("validated catalog entry = %s, %v, want available", entry.State, err)
	}
	verifyNativeCatalogClones(t, ctx, machines, entry, maximum)
}

func verifyNativeCatalogClones(t *testing.T, ctx context.Context, machines *utm.Provider, entry release.CatalogEntry, resources provider.Resources) {
	t.Helper()
	workspaceID := uuid.NewString()
	create := func() provider.Inventory {
		t.Helper()
		ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: workspaceID,
			Generation: 1, OwnershipMarker: uuid.NewString()}
		t.Cleanup(func() { cleanupEnrollmentVM(t, machines, ref) })
		vm, err := machines.Create(ctx, provider.CreateRequest{Ref: ref, OperationID: uuid.NewString(),
			ArtifactID: entry.ID, Release: entry.Artifact.Manifest.ReleaseID, Resources: resources, NetworkMode: "shared"})
		ref.InstanceID = vm.InstanceID
		if err != nil || vm.Ref != ref || vm.InstanceID == "" || vm.Power != provider.Stopped || vm.ArtifactID != entry.ID {
			t.Fatalf("create clone under %s = %+v (%v), want the stopped owned VM from artifact %s", ref.InstallationID, vm, err, entry.ID)
		}
		return vm
	}
	first, second := create(), create()
	if first.InstanceID == second.InstanceID || first.Ref.OwnershipMarker == second.Ref.OwnershipMarker {
		t.Fatal("the two installations reused a provider instance or ownership marker")
	}
	seen := make(map[string]bool)
	for _, vm := range []provider.Inventory{first, second} {
		disks := 0
		for _, resource := range vm.Resources {
			if seen[resource.ID] {
				t.Fatalf("both clones reference the same provider resource %s", resource.ID)
			}
			seen[resource.ID] = true
			if resource.Type != "disk" {
				continue
			}
			disks++
			if resource.Shared || resource.Immutable || resource.OwnershipMarker != vm.Ref.OwnershipMarker ||
				resource.Generation != vm.Ref.Generation {
				t.Fatalf("clone disk ownership = %+v, want a writable disk owned by %+v", resource, vm.Ref)
			}
		}
		if disks == 0 {
			t.Fatalf("clone %s has no owned disk", vm.InstanceID)
		}
	}
	remove := func(vm provider.Inventory) {
		t.Helper()
		gone, err := machines.Delete(ctx, provider.DeleteRequest{Ref: vm.Ref, OperationID: uuid.NewString()})
		if err != nil || gone.Power != provider.Deleted || len(gone.Resources) != 0 {
			t.Fatalf("delete clone %s = %s with %d resources (%v), want verified deletion", vm.InstanceID, gone.Power, len(gone.Resources), err)
		}
		if _, err := machines.Inspect(ctx, vm.Ref); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("inventory still reports clone %s after deletion: %v", vm.InstanceID, err)
		}
	}
	remove(first)
	survivor, err := machines.Inspect(ctx, second.Ref)
	if err != nil || survivor.Ref != second.Ref || survivor.Power != provider.Stopped {
		t.Fatalf("sibling clone after deleting %s = %+v (%v), want the original stopped VM %s", first.InstanceID, survivor, err, second.InstanceID)
	}
	verifyOwnedDisk(t, second, survivor)
	remove(second)
	t.Log("the same workspace ID in two installations received separate VM and disk ownership; deletion preserved the sibling")
}
