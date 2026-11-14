//go:build utm_release

package release_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
	_ "modernc.org/sqlite"
)

// TestUTMGuestEnrollment imports an actual Packer export, attaches one-use
// media, verifies an authenticated guest poll, and deletes the disposable VM.
func TestUTMGuestEnrollment(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_MUTATIONS") != "5.0.8" {
		t.Skip("set SHIFTBOX_TEST_UTM_MUTATIONS=5.0.8 to enable the disposable VM journey")
	}
	bundle, storage, host := os.Getenv("SHIFTBOX_TEST_UTM_BUNDLE"), os.Getenv("SHIFTBOX_TEST_UTM_STORAGE"), os.Getenv("SHIFTBOX_TEST_UTM_GUEST_HOST")
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(storage) || host == "" {
		t.Fatal("set the bundle, UTM storage, and guest-reachable host")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	const owner = "fixture-owner"
	const guestRelease = "development"
	dir := t.TempDir()
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(dir, "fixture.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "Fixture", Origin: "https://management.test",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: guestRelease, MinGuestRelease: guestRelease, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execUTMFixture(t, ctx, db, "UPDATE installations SET state='active'")
	execUTMFixture(t, ctx, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'fixture@example.test','administrator','active','local','setup','now','now',1)`, owner)
	catalogStore := release.NewStore(db)
	catalog := release.NewCatalog(catalogStore, filepath.Join(dir, "catalog"))
	artifactID, workspaceID, marker := uuid.NewString(), uuid.NewString(), uuid.NewString()
	manifest := enrollmentFixtureManifest(t, bundle, guestRelease)
	artifact, err := catalog.Import(ctx, artifactID, owner, time.Now(), filepath.Dir(bundle), strings.NewReader(manifest))
	if err != nil {
		t.Fatal(err)
	}
	// The fixture supplies an enrollment-only export. Mark it available in
	// this isolated database solely to drive the existing launch path.
	if err := catalogStore.CompleteImport(ctx, artifactID, artifact.Artifact.ManifestSHA256, true); err != nil {
		t.Fatal(err)
	}
	allocation := workspace.Allocation{CPU: 2, MemoryMiB: 4096, DiskMiB: 32768}
	resources := provider.Resources{CPU: 2, MemoryBytes: 4096 << 20, DiskBytes: 32768 << 20}
	machines, err := utm.NewProvider(catalog, dir, storage, resources, resources)
	if err != nil {
		t.Fatal(err)
	}
	store := workspace.NewStore(db)
	if err := store.SetCapacityBudget(ctx, id, allocation, 0, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := app.EnsureInstallationCA(dir, id, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, ca)
	bind := net.JoinHostPort(host, "0")
	listeners, err := app.ListenGuests(app.GuestListenerConfig{EnrollmentAddress: bind, PollAddress: bind, PublicHost: host},
		enrollment, ca, web.GuestCompatibility{Protocol: "1", CurrentRelease: guestRelease}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	listenerCtx, stop := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- listeners.Serve(listenerCtx) }()
	defer func() { stop(); <-served }()

	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	var op operation.Operation
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		op, err = workspace.AdmitLaunch(ctx, tx, journal, workspace.LaunchIntent{
			InstallationID: id, WorkspaceID: workspaceID, OwnerID: owner, TemplateID: uuid.NewString(), TemplateRevision: 1,
			RequestKey: "live-utm-enrollment", ArtifactID: artifactID, RepositoryURL: "https://github.com/example/project",
			DevcontainerPath: ".devcontainer/devcontainer.json", Allocation: allocation, OwnershipMarker: marker, NetworkPolicyJSON: `{"mode":"shared"}`,
		}, time.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.WorkspaceRef{InstallationID: id, WorkspaceID: workspaceID, Generation: 1, OwnershipMarker: marker}
	t.Cleanup(func() { cleanupEnrollmentVM(t, machines, ref) })
	worker := operation.Worker{Journal: journal, Name: "release-fixture", LeaseDuration: 4 * time.Minute,
		Executor: workspace.NewLaunchExecutor(store, machines, enrollment, dir, listeners.EnrollURL, listeners.PollURL, time.Now)}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatalf("provision %s: %v", workspaceID, err)
	}
	t.Logf("provisioned disposable workspace %s", workspaceID)
	started := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		err := worker.RunOnce(ctx, op.ID)
		current, readErr := journal.Get(ctx, op.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.Steps[1].Status == "succeeded" {
			t.Logf("authenticated guest enrolled in %s", time.Since(started).Round(time.Second))
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no authenticated guest poll for %s, steps %+v, last reconciliation error %v", workspaceID, current.Steps, err)
		case <-ticker.C:
		}
	}
}

func execUTMFixture(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func cleanupEnrollmentVM(t *testing.T, machines *utm.Provider, ref provider.WorkspaceRef) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	inventory, err := machines.Inspect(ctx, ref)
	if errors.Is(err, provider.ErrNotFound) {
		return
	}
	if err != nil {
		t.Errorf("inspect disposable workspace %s during cleanup: %v", ref.WorkspaceID, err)
		return
	}
	if inventory.Power == provider.Running {
		inventory, err = machines.Stop(ctx, provider.StopRequest{Ref: inventory.Ref, OperationID: "fixture-cleanup", ForceRetentionDelete: true})
		if err != nil {
			t.Errorf("stop disposable VM %s: %v", inventory.InstanceID, err)
			return
		}
	}
	result, err := machines.Delete(ctx, provider.DeleteRequest{Ref: inventory.Ref, OperationID: "fixture-cleanup"})
	if err != nil || result.Power != provider.Deleted {
		t.Errorf("delete disposable VM %s: power %s, error %v", inventory.InstanceID, result.Power, err)
	}
}

func enrollmentFixtureManifest(t *testing.T, bundle, guestRelease string) string {
	t.Helper()
	var resources []release.ArtifactResource
	err := filepath.WalkDir(bundle, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		kind := "vm"
		if filepath.Ext(path) == ".qcow2" {
			kind = "disk"
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			return err
		}
		relative, err := filepath.Rel(filepath.Dir(bundle), path)
		if err != nil {
			return err
		}
		resources = append(resources, release.ArtifactResource{Path: relative, Type: kind, SHA256: fmt.Sprintf("%x", digest.Sum(nil))})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("disposable enrollment test metadata")))
	encoded, err := json.Marshal(release.ArtifactManifest{
		SchemaVersion: 1, Architecture: "amd64", ReleaseID: "enrollment-fixture", PlatformVersion: "24.04", ProtocolVersion: "1",
		Guest:      release.ArtifactComponent{Version: guestRelease, SHA256: digest},
		IDE:        release.ArtifactComponent{Version: "fixture-only", SHA256: digest},
		Provenance: release.ArtifactProvenance{Source: "local Packer enrollment fixture", Revision: "enrollment-fixture"}, Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
