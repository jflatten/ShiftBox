package release_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/testsupport"
)

// TestManagedCatalogRestoresPublishedArtifact checks that a restarted catalog
// can stage a validated release after the original import source disappears.
func TestManagedCatalogRestoresPublishedArtifact(t *testing.T) {
	ctx := context.Background()
	db := openCatalogDatabase(t, "sqlite")
	root, manifest := artifactFixture(t)
	storage := t.TempDir()
	catalog := release.NewCatalog(release.NewStore(db), storage)
	id, actor := testsupport.ID(201), testsupport.ID(1)
	entry, err := catalog.Import(ctx, id, actor, time.Now(), root, bytes.NewReader(manifest))
	if err != nil || entry.State != "validating" {
		t.Fatalf("catalog import = %+v, %v, want validating", entry, err)
	}
	if _, err := catalog.Stage(ctx, id, t.TempDir(), "workspace.utm"); !errors.Is(err, release.ErrCatalogConflict) {
		t.Errorf("stage unvalidated artifact = %v, want catalog conflict", err)
	}
	if err := catalog.ValidateImport(ctx, id, func(_ context.Context, artifact release.Artifact) error {
		if artifact.ManifestSHA256 != entry.Artifact.ManifestSHA256 || !filepath.IsAbs(artifact.Bundle) {
			t.Fatalf("boot validation received %+v, want exact managed bundle", artifact)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	catalog = release.NewCatalog(release.NewStore(db), storage)
	staged, err := catalog.Stage(ctx, id, t.TempDir(), "workspace.utm")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(filepath.Join(staged.Bundle, "Data", "platform.qcow2"))
	if err != nil || string(disk) != "reference disk" {
		t.Fatalf("staged catalog disk = %q, %v, want reference disk", disk, err)
	}
}

// TestManagedCatalogRetainsUncertainValidation keeps the release unavailable
// when the boot validator cannot confirm the outcome or the files are absent.
func TestManagedCatalogRetainsUncertainValidation(t *testing.T) {
	ctx := context.Background()
	store := release.NewStore(openCatalogDatabase(t, "sqlite"))
	storage := t.TempDir()
	catalog := release.NewCatalog(store, storage)
	source, manifest := artifactFixture(t)
	id := testsupport.ID(201)
	entry, err := catalog.Import(ctx, id, testsupport.ID(1), time.Now(), source, bytes.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	uncertain := errors.New("lost boot status")
	if err := catalog.ValidateImport(ctx, id, func(context.Context, release.Artifact) error { return uncertain }); !errors.Is(err, uncertain) {
		t.Errorf("validation error = %v, want lost boot status", err)
	}
	managed := filepath.Join(storage, entry.Artifact.ManifestSHA256)
	if err := os.RemoveAll(managed); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateImport(ctx, id, func(context.Context, release.Artifact) error {
		t.Error("boot check ran without managed files")
		return nil
	}); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Errorf("validation without managed files = %v, want invalid artifact", err)
	}
	entry, err = store.Get(ctx, id)
	if err != nil || entry.State != "validating" {
		t.Errorf("state after uncertain validation = %s, %v, want validating", entry.State, err)
	}
}

// TestManagedCatalogBootsDisposableCopy verifies that a boot check can write
// to its VM disk without touching the catalog or leaving a scratch bundle.
func TestManagedCatalogBootsDisposableCopy(t *testing.T) {
	ctx := t.Context()
	store := release.NewStore(openCatalogDatabase(t, "sqlite"))
	storage := t.TempDir()
	catalog := release.NewCatalog(store, storage)
	source, manifest := artifactFixture(t)
	id := testsupport.ID(201)
	entry, err := catalog.Import(ctx, id, testsupport.ID(1), time.Now(), source, bytes.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	var checkedBundle string
	err = catalog.ValidateImport(ctx, id, func(_ context.Context, artifact release.Artifact) error {
		checkedBundle = artifact.Bundle
		return os.WriteFile(filepath.Join(artifact.Bundle, "Data", "platform.qcow2"), []byte("booted copy"), 0600)
	})
	if err != nil {
		t.Fatalf("validate disposable boot = %v, want successful validation", err)
	}
	if _, err := os.Stat(checkedBundle); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("boot validation bundle after check = %v, want removed copy", err)
	}
	immutableDisk := filepath.Join(storage, entry.Artifact.ManifestSHA256, diskPath)
	disk, err := os.ReadFile(immutableDisk)
	if err != nil || string(disk) != "reference disk" {
		t.Errorf("catalog disk after boot = %q, %v, want original bytes", disk, err)
	}
	if _, err := catalog.Stage(ctx, id, t.TempDir(), "workspace.utm"); err != nil {
		t.Errorf("stage after boot = %v, want validated catalog image", err)
	}
}

// TestManagedCatalogReconcilesPublication checks that matching durable input
// can recover a prior publish and that corrupted content cannot be staged.
func TestManagedCatalogReconcilesPublication(t *testing.T) {
	ctx := context.Background()
	db := openCatalogDatabase(t, "sqlite")
	root, manifest := artifactFixture(t)
	storage := t.TempDir()
	catalog := release.NewCatalog(release.NewStore(db), storage)
	id, actor := testsupport.ID(201), testsupport.ID(1)
	entry, err := catalog.Import(ctx, id, actor, time.Now(), root, bytes.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	// A committed validating record can survive an interruption before the
	// staged directory is published. Recreate that state before retrying.
	managed := filepath.Join(storage, entry.Artifact.ManifestSHA256)
	if err := os.RemoveAll(managed); err != nil {
		t.Fatal(err)
	}
	repeat, err := catalog.Import(ctx, id, actor, time.Now(), root, bytes.NewReader(manifest))
	if err != nil || repeat.ID != entry.ID || !repeat.ImportedAt.Equal(entry.ImportedAt) {
		t.Fatalf("catalog import retry = %+v, %v, want original record", repeat, err)
	}
	changedManifest := append(bytes.Clone(manifest), '\n')
	if _, err := catalog.Import(ctx, id, actor, time.Now(), root, bytes.NewReader(changedManifest)); !errors.Is(err, release.ErrCatalogConflict) {
		t.Errorf("reuse import ID with a different digest = %v, want catalog conflict", err)
	}
	if err := release.NewStore(db).CompleteImport(ctx, id, entry.Artifact.ManifestSHA256, true); err != nil {
		t.Fatal(err)
	}
	writeArtifactFile(t, managed, diskPath, []byte("corrupt catalog disk"))
	if _, err := catalog.Stage(ctx, id, t.TempDir(), "workspace.utm"); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Errorf("stage modified catalog data = %v, want integrity failure", err)
	}
}

// TestManagedCatalogRejectsManifestTampering checks that staging uses the
// digest pinned in the database even when the altered manifest parses.
func TestManagedCatalogRejectsManifestTampering(t *testing.T) {
	ctx := context.Background()
	store := release.NewStore(openCatalogDatabase(t, "sqlite"))
	source, manifest := artifactFixture(t)
	storage := t.TempDir()
	catalog := release.NewCatalog(store, storage)
	id, actor := testsupport.ID(201), testsupport.ID(1)
	entry, err := catalog.Import(ctx, id, actor, time.Now(), source, bytes.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteImport(ctx, id, entry.Artifact.ManifestSHA256, true); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(manifest, []byte("reference-1"), []byte("reference-2"), 1)
	managed := filepath.Join(storage, entry.Artifact.ManifestSHA256)
	writeArtifactFile(t, managed, "manifest.json", changed)
	stageRoot := t.TempDir()
	if _, err := catalog.Stage(ctx, id, stageRoot, "workspace.utm"); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Errorf("stage changed manifest = %v, want integrity failure", err)
	}
	if items, err := os.ReadDir(stageRoot); err != nil || len(items) != 0 {
		t.Errorf("workspace staging after tampering = %v, %v, want empty directory", items, err)
	}
}
