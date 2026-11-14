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

// TestUTMCatalogValidation exercises the candidate boot check used before
// catalog publication and verifies that the disposable workspace is gone.
func TestUTMCatalogValidation(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_MUTATIONS") != "5.0.8" {
		t.Skip("set SHIFTBOX_TEST_UTM_MUTATIONS=5.0.8 to enable native catalog validation")
	}
	bundle, storage, host := os.Getenv("SHIFTBOX_TEST_UTM_BUNDLE"), os.Getenv("SHIFTBOX_TEST_UTM_STORAGE"), os.Getenv("SHIFTBOX_TEST_UTM_GUEST_HOST")
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(storage) || host == "" {
		t.Fatal("set the bundle, UTM storage, and guest-reachable host")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
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
}
