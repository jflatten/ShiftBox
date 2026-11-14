//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
	_ "modernc.org/sqlite"
)

// TestUTMGuestEnrollment imports an actual Packer export, attaches one-use
// media, verifies an authenticated guest poll, then checks the offboarding
// stop and retained disk before deleting the disposable VM.
func TestUTMGuestEnrollment(t *testing.T) {
	runUTMGuestFixture(t, nil, false)
}

// TestUTMExpiredGuestRecovery verifies fresh provider enrollment and preserved
// disk identity after a real short-lived guest certificate expires offline.
func TestUTMExpiredGuestRecovery(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_EXPIRED_GUEST") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_EXPIRED_GUEST=1 for native recovery")
	}
	runUTMGuestFixture(t, nil, true)
}

func runUTMGuestFixture(t *testing.T, maintenance *nativeMaintenanceFixture, recoverExpiredGuest bool) {
	t.Helper()
	if os.Getenv("SHIFTBOX_TEST_UTM_MUTATIONS") != "5.0.8" {
		t.Skip("set SHIFTBOX_TEST_UTM_MUTATIONS=5.0.8 to enable the disposable VM journey")
	}
	bundle, storage, host := os.Getenv("SHIFTBOX_TEST_UTM_BUNDLE"), os.Getenv("SHIFTBOX_TEST_UTM_STORAGE"), os.Getenv("SHIFTBOX_TEST_UTM_GUEST_HOST")
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(storage) || host == "" {
		t.Fatal("set the bundle, UTM storage, and guest-reachable host")
	}
	// Two maintenance trials each add a native boot and several guest polls.
	timeout := 8 * time.Minute
	if maintenance != nil {
		timeout = 18 * time.Minute
	}
	if recoverExpiredGuest {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	owner := uuid.NewString()
	manifest, err := os.ReadFile(filepath.Join(filepath.Dir(bundle), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata release.ArtifactManifest
	if err := json.Unmarshal(manifest, &metadata); err != nil {
		t.Fatal(err)
	}
	if maintenance != nil {
		maintenance.prepare(t, ctx, metadata)
	}
	guestRelease := metadata.Guest.Version
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
	workspaceID, marker := uuid.NewString(), uuid.NewString()
	artifact, err := catalog.ImportLocal(ctx, owner, time.Now(), filepath.Dir(bundle))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Artifact.Bundle != filepath.Base(bundle) {
		t.Fatalf("manifest describes %s, expected %s", artifact.Artifact.Bundle, filepath.Base(bundle))
	}
	artifactID := artifact.ID
	// Make the candidate available only in this isolated database so the
	// production launch path can boot it for validation.
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
	bind := net.JoinHostPort(host, "0")
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	state := &app.ControllerState{DB: db, GuestCA: ca}
	state.Installation.InstallationID = id
	state.Installation.Settings.ManagementOrigin = "https://management.test"
	state.Installation.Settings.IDEHostPattern = "{workspace}.ide.test"
	state.Installation.Settings.PreviewHostPattern = "{service}--{workspace}.preview.test"
	startRuntime := func(authority *guest.Authority) (*proxy.Registry, func()) {
		t.Helper()
		registry := &proxy.Registry{}
		listeners := app.GuestListenerConfig{
			EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: host,
			Tunnels: &proxy.Tunnels{Registry: registry},
		}
		if maintenance != nil {
			listeners.GuestUpdates = maintenance.load
		}
		state.GuestCA = authority
		runtime, err := app.ListenWorkspaceRuntime(state, listeners,
			web.GuestCompatibility{Protocol: metadata.ProtocolVersion, CurrentRelease: guestRelease}, journal, machines, dir, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		runCtx, stop := context.WithCancel(ctx)
		served := make(chan error, 1)
		go func() { served <- runtime.Serve(runCtx) }()
		return registry, func() {
			stop()
			if err := <-served; !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("workspace runtime stopped: %v", err)
			}
			runtime.Close()
		}
	}
	issuer := ca
	if recoverExpiredGuest {
		// Keep the genuine CA DER, subject, and signing key. This copy caps only
		// initial certificate issuance so the native guest can expire offline.
		shortIssuer, shortCertificate := *ca, *ca.Certificate
		shortCertificate.NotAfter = time.Now().Add(4 * time.Minute).Truncate(time.Second)
		shortIssuer.Certificate = &shortCertificate
		issuer = &shortIssuer
	}
	registry, stopRuntime := startRuntime(issuer)
	defer func() { stopRuntime() }()

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
	started := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		current, readErr := journal.Get(ctx, op.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.Steps[1].Status == "succeeded" {
			t.Logf("authenticated guest enrolled in %s", time.Since(started).Round(time.Second))
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no authenticated guest poll for %s, steps %+v", workspaceID, current.Steps)
		case <-ticker.C:
		}
	}
	before, err := machines.Inspect(ctx, ref)
	if err != nil || before.Power != provider.Running {
		t.Fatalf("provider after enrollment = %s (%v), want running", before.Power, err)
	}
	if maintenance != nil {
		maintenance.verify(t, ctx, db, journal, machines, before, op.ID, owner, metadata)
		return
	}
	if recoverExpiredGuest {
		stopped := stopUTMEnrolledFixture(t, ctx, db, journal, machines, before, op.ID, owner)
		stopRuntime()
		stopRuntime = func() {}
		prior := waitForNativeGuestExpiry(t, ctx, db, stopped.Ref)
		_, stopRuntime = startRuntime(ca)
		verifyNativeGuestRecovery(t, ctx, db, journal, machines, stopped, owner, prior)
		return
	}
	stopID := disableEnrollmentFixtureOwner(t, db, journal, registry, owner, workspaceID)
	for {
		stop, err := journal.Get(ctx, stopID)
		if err != nil {
			t.Fatal(err)
		}
		switch stop.Status {
		case "succeeded":
			after, err := machines.Inspect(ctx, before.Ref)
			if err != nil || after.Power != provider.Stopped {
				t.Fatalf("provider after offboarding = %s (%v), want stopped", after.Power, err)
			}
			verifyOwnedDisk(t, before, after)
			var compute, disk string
			if err := db.QueryRowContext(ctx, "SELECT cpu_memory_state,disk_state FROM reservations WHERE workspace_id=$1",
				workspaceID).Scan(&compute, &disk); err != nil || compute != "released" || disk != "reserved" {
				t.Fatalf("offboarding reservation = %s/%s (%v), want released/reserved", compute, disk, err)
			}
			t.Log("provider confirmed offboarding stop with the same owned disk")
			return
		case "failed", "cancelled":
			t.Fatalf("offboarding stop = %s, steps %+v", stop.Status, stop.Steps)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("provider did not confirm offboarding stop: %+v", stop.Steps)
		case <-ticker.C:
		}
	}
}

func disableEnrollmentFixtureOwner(t *testing.T, db *sql.DB, journal *operation.Journal, registry *proxy.Registry, owner, workspaceID string) string {
	t.Helper()
	ctx := t.Context()
	const administrator = "fixture-offboarding-admin"
	const origin = "https://management.test"
	execUTMFixture(t, ctx, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'operator@example.test','administrator','active','local','setup','now','now',1)`, administrator)
	store := auth.NewStore(db)
	session, token, err := auth.NewSession(uuid.NewString(), administrator, "normal", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return store.SaveSession(ctx, tx, session) }); err != nil {
		t.Fatal(err)
	}
	handler := web.UpdateMembershipHandler(db, origin, time.Now, registry, journal)
	request := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/admin/users/"+owner, strings.NewReader(`{"membershipState":"disabled"}`))
	request.SetPathValue("userId", owner)
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	request.Header.Set("Origin", origin)
	request.Header.Set("If-Match", `"1"`)
	request.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var result struct {
		StopOperations []workspace.OffboardingStop
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); response.Code != http.StatusOK || err != nil ||
		len(result.StopOperations) != 1 || result.StopOperations[0].WorkspaceID != workspaceID || result.StopOperations[0].OperationID == "" {
		t.Fatalf("offboarding response = %d, %+v (%v), want one stop for %s", response.Code, result, err, workspaceID)
	}
	return result.StopOperations[0].OperationID
}

func verifyOwnedDisk(t *testing.T, before, after provider.Inventory) {
	t.Helper()
	disks := make(map[string]provider.OwnedResource)
	for _, resource := range before.Resources {
		if resource.Type == "disk" && !resource.Shared && !resource.Immutable {
			disks[resource.ID] = resource
		}
	}
	if len(disks) == 0 {
		t.Fatal("provider did not identify an owned disk before shutdown")
	}
	for _, resource := range after.Resources {
		if previous, ok := disks[resource.ID]; ok && resource.OwnershipMarker == previous.OwnershipMarker &&
			resource.Generation == previous.Generation && resource.Type == "disk" && !resource.Shared && !resource.Immutable {
			delete(disks, resource.ID)
		}
	}
	if len(disks) != 0 {
		t.Errorf("shutdown lost owned disk identity: %v", disks)
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
