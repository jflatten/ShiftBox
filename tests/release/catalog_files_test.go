package release_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/testsupport"
)

// TestCatalogImportsLocalFolder verifies that setup can retry the same local
// export after a restart while a changed manifest cannot replace the release.
func TestCatalogImportsLocalFolder(t *testing.T) {
	ctx := t.Context()
	store := release.NewStore(openCatalogDatabase(t, "sqlite"))
	storage := t.TempDir()
	source, manifest := artifactFixture(t)
	manifestPath := filepath.Join(source, "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	actor, now := testsupport.ID(1), time.Now()
	first, err := release.NewCatalog(store, storage).ImportLocal(ctx, actor, now, source)
	if err != nil || first.ID == "" || first.State != "validating" || first.IsDefault {
		t.Fatalf("ImportLocal = %+v, %v, want a release awaiting validation", first, err)
	}
	catalog := release.NewCatalog(store, storage)
	retried, err := catalog.ImportLocal(ctx, actor, now.Add(time.Minute), source)
	if err != nil || retried.ID != first.ID || retried.Artifact.ManifestSHA256 != first.Artifact.ManifestSHA256 || !retried.ImportedAt.Equal(first.ImportedAt) {
		t.Fatalf("ImportLocal retry = %+v, %v, want original release %s", retried, err, first.ID)
	}
	if err := os.WriteFile(manifestPath, append(manifest, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ImportLocal(ctx, actor, now, source); !errors.Is(err, release.ErrCatalogConflict) {
		t.Errorf("ImportLocal with changed manifest = %v, want conflict", err)
	}
	if _, err := catalog.Stage(ctx, first.ID, t.TempDir(), "workspace.utm"); !errors.Is(err, release.ErrCatalogConflict) {
		t.Errorf("Stage after local import = %v, want validation required", err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(storage, first.Artifact.ManifestSHA256, "manifest.json"), manifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.ImportLocal(ctx, actor, now, source); !errors.Is(err, release.ErrInvalidArtifact) {
		t.Errorf("ImportLocal with a linked manifest = %v, want invalid artifact", err)
	}
}

// TestManagedCatalogRestoresPublishedArtifact checks that a restarted catalog
// resumes pending boot validation and reuses an available release without
// starting another boot when the import completion reply was lost.
func TestManagedCatalogRestoresPublishedArtifact(t *testing.T) {
	ctx := t.Context()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "catalog.db")}
	open := func() *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	root, manifest := artifactFixture(t)
	storage := t.TempDir()
	catalog := release.NewCatalog(release.NewStore(db), storage)
	restart := func() {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		catalog = release.NewCatalog(release.NewStore(db), storage)
	}
	id, actor := testsupport.ID(201), testsupport.ID(1)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'owner@example.test','administrator','active','local','setup','now','now',1)`, actor); err != nil {
		t.Fatal(err)
	}
	entry, err := catalog.Import(ctx, id, actor, time.Now(), root, bytes.NewReader(manifest))
	if err != nil || entry.State != "validating" {
		t.Fatalf("catalog import = %+v, %v, want validating", entry, err)
	}
	if _, err := catalog.Stage(ctx, id, t.TempDir(), "workspace.utm"); !errors.Is(err, release.ErrCatalogConflict) {
		t.Errorf("stage unvalidated artifact = %v, want catalog conflict", err)
	}
	uncertain := errors.New("lost boot result")
	if err := catalog.ValidateImport(ctx, id, func(context.Context, release.Artifact) error { return uncertain }); !errors.Is(err, uncertain) {
		t.Fatalf("interrupted boot validation = %v, want unknown boot result", err)
	}
	restart()
	pending, err := release.NewStore(db).Get(ctx, id)
	if err != nil || pending.State != "validating" || pending.IsDefault {
		t.Fatalf("catalog entry after interrupted validation = %s, default=%t (%v), want validating and unavailable",
			pending.State, pending.IsDefault, err)
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
	restart()
	if err := catalog.ValidateImport(ctx, id, func(context.Context, release.Artifact) error {
		t.Fatal("replaying a committed catalog validation started another boot")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
