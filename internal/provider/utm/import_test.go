package utm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestImportRefusesUntrackedBundle prevents a duplicate import if an earlier
// attempt wrote the destination but did not establish its ownership in UTM.
func TestImportRefusesUntrackedBundle(t *testing.T) {
	ref := ownedDeleteRequest().Ref
	ref.InstanceID = ""
	req := provider.CreateRequest{Ref: ref, OperationID: "create", ArtifactID: "artifact", Release: "release",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 512 << 20, DiskBytes: 64 << 20}, NetworkMode: "shared"}
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), managedBundleName(ref))
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, filepath.Base(source)), 0700); err != nil {
		t.Fatal(err)
	}
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) {
			return provider.Inventory{}, provider.ErrNotFound
		},
	}
	_, err := p.ImportManagedVM(context.Background(), req, source, root)
	if !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("ImportManagedVM after an incomplete attempt = %v, want uncertain outcome", err)
	}
}

// TestImportReusesOwnedVM verifies that a matching VM discovered on retry is
// reused only when its artifact and requested configuration still match.
func TestImportReusesOwnedVM(t *testing.T) {
	ref := ownedDeleteRequest().Ref
	id := ref.InstanceID
	ref.InstanceID = ""
	req := provider.CreateRequest{Ref: ref, OperationID: "create", ArtifactID: "artifact", Release: "release",
		Resources: provider.Resources{CPU: 2, MemoryBytes: 512 << 20, DiskBytes: 64 << 20}, NetworkMode: "shared"}
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), managedBundleName(ref))
	if err := os.Mkdir(filepath.Join(root, filepath.Base(source)), 0700); err != nil {
		t.Fatal(err)
	}
	found := provider.Inventory{Ref: ownedDeleteRequest().Ref, InstanceID: id, ArtifactID: req.ArtifactID,
		Release: req.Release, NetworkMode: req.NetworkMode, Power: provider.Stopped, Effective: req.Resources,
		Resources: []provider.OwnedResource{
			{ID: id, Type: "vm"}, {ID: id + "/disk", Type: "disk"}, {ID: id + "/media", Type: "media"},
		}}
	p := PowerController{
		inspect: func(context.Context, provider.WorkspaceRef) (provider.Inventory, error) { return found, nil },
	}
	got, err := p.ImportManagedVM(context.Background(), req, source, root)
	if err != nil || got.InstanceID != id {
		t.Fatalf("ImportManagedVM retry = %s, %v, want %s", got.InstanceID, err, id)
	}
	complete := found.Resources
	found.Resources = found.Resources[:2]
	if _, err := p.ImportManagedVM(context.Background(), req, source, root); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("ImportManagedVM retry without recovery drive = %v, want uncertain outcome", err)
	}
	found.Resources = complete
	found.Release = "different"
	if _, err := p.ImportManagedVM(context.Background(), req, source, root); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Errorf("ImportManagedVM retry with different release = %v, want uncertain outcome", err)
	}
}

// checkLiveImport verifies that UTM creates a separate stopped VM, applies
// ownership and compute settings, and reuses the same copy on retry.
func checkLiveImport(t *testing.T, controller *PowerController, root string, fixture func(string, string, ...string) (string, error)) {
	t.Helper()
	ref := provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	name := managedBundleName(ref)
	fixtureID, err := fixture("source", "ShiftBox-ImportSource-"+ref.WorkspaceID)
	if err != nil {
		t.Fatalf("create import source: %v", err)
	}
	defer func() {
		if _, err := fixture("delete source", fixtureID); err != nil {
			t.Errorf("remove import source: %v", err)
		}
	}()

	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "reference.utm")
	if _, err := fixture("export", fixtureID, source); err != nil {
		t.Fatalf("export import source: %v", err)
	}
	sourceFiles := bundleDigests(t, source)
	manifest := fixtureImportManifest(t, sourceRoot, sourceFiles)
	stageRoot := t.TempDir()
	destination := filepath.Join(root, name)
	req := provider.CreateRequest{Ref: ref, OperationID: "fixture-create", ArtifactID: "fixture", Release: "fixture",
		Resources: provider.Resources{CPU: 3, MemoryBytes: 768 << 20, DiskBytes: 64 << 20}, NetworkMode: "shared"}
	got, err := controller.ImportCatalogArtifact(context.Background(), req, sourceRoot, manifest, stageRoot, root)
	if got.InstanceID != "" {
		defer func() {
			_, err := controller.DeleteManagedVM(context.Background(), provider.DeleteRequest{Ref: got.Ref, OperationID: "fixture-cleanup"}, destination)
			if err != nil {
				t.Errorf("remove imported fixture: %v", err)
			}
		}()
	}
	if err != nil || got.InstanceID == fixtureID || len(got.Resources) != 3 {
		t.Fatalf("imported fixture = %+v, %v, want new stopped VM with its own disk and recovery drive", got, err)
	}
	repeat, err := controller.ImportCatalogArtifact(context.Background(), req, sourceRoot, manifest, stageRoot, root)
	if err != nil || repeat.InstanceID != got.InstanceID {
		t.Fatalf("repeated import = %s, %v, want %s", repeat.InstanceID, err, got.InstanceID)
	}
	checkLiveDeletion(t, controller, got.Ref, destination)
	if got := bundleDigests(t, source); !maps.Equal(got, sourceFiles) {
		t.Fatal("importing or deleting the workspace changed the exported source")
	}
}

// fixtureImportManifest records the native export files in the catalog format.
func fixtureImportManifest(t *testing.T, root string, files map[string][32]byte) string {
	t.Helper()
	component := release.ArtifactComponent{Version: "fixture", SHA256: strings.Repeat("0", 64)}
	manifest := release.ArtifactManifest{SchemaVersion: 1, ReleaseID: "fixture",
		Architecture: "amd64", PlatformVersion: "fixture", ProtocolVersion: "1", Guest: component, IDE: component,
		Provenance: release.ArtifactProvenance{Source: "native UTM test fixture", Revision: "fixture"}}
	for path, sum := range files {
		kind := "vm"
		if filepath.Ext(path) == ".qcow2" {
			kind = "disk"
		}
		manifest.Resources = append(manifest.Resources, release.ArtifactResource{
			Path: "reference.utm/" + path, Type: kind, SHA256: hex.EncodeToString(sum[:]),
		})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

// bundleDigests records each file in the disposable source without reading
// or editing the registered UTM bundle.
func bundleDigests(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	err := fs.WalkDir(os.DirFS(root), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file, err := os.Open(filepath.Join(root, path))
		if err != nil {
			return err
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			return err
		}
		result[path] = [32]byte(digest.Sum(nil))
		return nil
	})
	if err != nil || len(result) == 0 {
		t.Fatalf("read source bundle digests: %v", err)
	}
	return result
}
