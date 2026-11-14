package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestWorkspaceRuntimeRunsMaintenance drives a persisted campaign through the
// real listeners and both workers with an enrolled guest client.
func TestWorkspaceRuntimeRunsMaintenance(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "workspace.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const installation = "99999999-0000-4000-8000-000000000001"
	const workspaceID = "99999999-0000-4000-8000-000000000003"
	for _, query := range []string{
		`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,
			active_release,min_guest_release,created_at,updated_at,version)
			VALUES('99999999-0000-4000-8000-000000000001','active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','fixture','previous','now','now',1)`,
		`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
			VALUES('owner','owner@example.test','administrator','active','local','setup','now','now',1)`,
		`INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,
			digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
			VALUES('artifact','fixture','amd64','test','fixture','1','[]','{}','{}','{}',
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available')`,
		`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,
			cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,
			provider_instance_id,created_at,updated_at,version)
			VALUES('99999999-0000-4000-8000-000000000003','99999999-0000-4000-8000-000000000001','owner','template',1,'artifact','https://github.com/example/project','devcontainer.json',
			2,1024,40,'{"mode":"shared"}','running','running',1,'owned','vm','now','now',1)`,
		`INSERT INTO guest_registrations(workspace_id,generation,state)
			VALUES('99999999-0000-4000-8000-000000000003',1,'eligible')`,
		`INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
			VALUES('fixture-provision','99999999-0000-4000-8000-000000000003','provision',1,'owner','fixture-enrollment','running','now',1)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	issued := time.Now().Add(-time.Hour)
	var campaign operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		campaign, err = workspace.ScheduleMaintenance(ctx, tx, journal, installation, "owner", "fixture-maintenance-request",
			"fixture", issued.Add(time.Minute), issued)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := EnsureInstallationCA(dir, installation, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := maintenanceGuestClient(t, db, ca, provider.WorkspaceRef{InstallationID: installation, WorkspaceID: workspaceID,
		Generation: 1, OwnershipMarker: "owned", InstanceID: "vm"})
	binary := []byte("signed runtime fixture")
	metadata := release.GuestUpdateMetadata{Release: "fixture", Protocol: "1", Size: int64(len(binary)), Digest: sha256.Sum256(binary)}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := release.GuestUpdate{Metadata: metadata, Binary: binary, Signature: ed25519.Sign(private, metadata.SignedBytes())}
	state := &ControllerState{DB: db, GuestCA: ca, Installation: ActiveInstallation{InstallationID: installation,
		Settings: config.InstallationSettings{ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test",
			PreviewHostPattern: "{service}--{workspace}.preview.test"}}}
	const bind = "127.0.0.1:0"
	runtime, err := ListenWorkspaceRuntime(state, GuestListenerConfig{EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: "127.0.0.1",
		Tunnels: &proxy.Tunnels{Registry: &proxy.Registry{}}, GuestUpdates: func(context.Context, string) (release.GuestUpdate, error) {
			return artifact, nil
		}}, web.GuestCompatibility{Protocol: "1", CurrentRelease: "fixture", PreviousRelease: "previous"}, journal, fake.New(), dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("workspace runtime shutdown = %v, want cancellation", err)
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	poll := func(releaseID string, receipt *guest.OperationReport, version int64) *guest.PollOperation {
		t.Helper()
		input := map[string]any{"guestRelease": releaseID, "protocolVersion": "1"}
		if receipt != nil {
			input["operationReport"], input["lastOperationId"], input["lastOperationVersion"] = receipt, receipt.ID, version
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(runtime.listeners.PollURL, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("guest poll returned %d: %s", response.StatusCode, body)
		}
		var result struct{ Operation *guest.PollOperation }
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result.Operation
	}
	waitCommand := func(command string, receipt *guest.OperationReport, version int64) *guest.PollOperation {
		t.Helper()
		for {
			if op := poll("previous", receipt, version); op != nil {
				if op.Command != command {
					t.Fatalf("guest command = %q, want %q", op.Command, command)
				}
				return op
			}
			select {
			case <-ctx.Done():
				t.Fatalf("campaign never dispatched %s", command)
			case <-ticker.C:
			}
		}
	}
	stage := waitCommand("stage-guest-update", nil, 0)
	pollURL, err := url.Parse(runtime.listeners.PollURL)
	if err != nil {
		t.Fatal(err)
	}
	pollURL.Path = guest.UpdateDownloadPath
	response, err := client.Get(pollURL.String())
	if err != nil {
		t.Fatal(err)
	}
	download, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	signature, sigErr := base64.StdEncoding.DecodeString(response.Header.Get("X-ShiftBox-Guest-Signature"))
	if response.StatusCode != http.StatusOK || readErr != nil || !bytes.Equal(download, binary) ||
		sigErr != nil || !ed25519.Verify(public, metadata.SignedBytes(), signature) {
		t.Fatalf("assigned update download = %d/%q (%v), want signed release", response.StatusCode, download, readErr)
	}
	receipt := &guest.OperationReport{ID: stage.ID, Generation: 1, Status: "succeeded", Step: "update.stage", Attempt: 1}
	receipt.Evidence.UpdateDigest = hex.EncodeToString(metadata.Digest[:])
	activate := waitCommand("activate-guest-update", receipt, stage.Version)
	receipt.Step = "update.activate"
	if op := poll("fixture", receipt, activate.Version); op != nil {
		t.Errorf("guest activation returned unexpected command %q", op.Command)
	}
	for {
		op, err := journal.Get(ctx, campaign.ID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("persisted maintenance campaign never completed")
		case <-ticker.C:
		}
	}
}

func maintenanceGuestClient(t *testing.T, db *sql.DB, ca *guest.Authority, ref provider.WorkspaceRef) *http.Client {
	t.Helper()
	ctx, now := t.Context(), time.Now()
	enrollment := guest.NewEnrollment(db, ca)
	grant, err := enrollment.Mint(ctx, "fixture-provision", provider.Inventory{
		Ref: ref, InstanceID: ref.InstanceID, ObservedAt: now, Power: provider.Stopped}, now)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(nil, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := enrollment.Enroll(ctx, grant, csr, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE operations SET status='succeeded' WHERE id='fixture-provision'"); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: guest.ClientTLS(tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}, ca, "127.0.0.1")}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}
}

// TestWorkspaceRuntimeClosesListenersOnWorkerFailure verifies that failure of
// the operation database cannot leave guest endpoints running independently.
func TestWorkspaceRuntimeClosesListenersOnWorkerFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "workspace.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	installation := uuid.NewString()
	ca, err := EnsureInstallationCA(dir, installation, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state := &ControllerState{DB: db, GuestCA: ca, Installation: ActiveInstallation{InstallationID: installation,
		Settings: config.InstallationSettings{ManagementOrigin: "https://management.test",
			IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test"}}}
	const bind = "127.0.0.1:0"
	runtime, err := ListenWorkspaceRuntime(state, GuestListenerConfig{
		EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: "127.0.0.1",
		Tunnels: &proxy.Tunnels{Registry: &proxy.Registry{}},
	}, web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"},
		operation.NewJournal(db, time.Now, uuid.NewString), fake.New(), dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	// The fresh database intentionally lacks the journal schema.
	if err := runtime.Serve(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Serve with unavailable journal = %v, want database error", err)
	}
	enroll, err := url.Parse(runtime.listeners.EnrollURL)
	if err != nil {
		t.Fatal(err)
	}
	poll, err := url.Parse(runtime.listeners.PollURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{enroll.Host, poll.Host, runtime.listeners.TunnelAddress} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Errorf("bind %s after worker failure = %v, want released listener", address, err)
			continue
		}
		listener.Close()
	}
}
