package app

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider/fake"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestWorkspaceRuntimeRunsMaintenance proves that a campaign persisted before
// service startup advances through both workers without manual step execution.
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
	state := &ControllerState{DB: db, GuestCA: ca, Installation: ActiveInstallation{InstallationID: installation,
		Settings: config.InstallationSettings{ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test",
			PreviewHostPattern: "{service}--{workspace}.preview.test"}}}
	const bind = "127.0.0.1:0"
	runtime, err := ListenWorkspaceRuntime(state, GuestListenerConfig{EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: "127.0.0.1",
		Tunnels: &proxy.Tunnels{Registry: &proxy.Registry{}}, GuestUpdates: func(context.Context, string) (release.GuestUpdate, error) {
			return release.GuestUpdate{}, release.ErrGuestUpdateUnavailable
		}}, web.GuestCompatibility{Protocol: "1", CurrentRelease: "fixture"}, journal, fake.New(), dir, time.Now)
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
	for {
		var stage bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM maintenance_targets t JOIN operations o ON o.id=t.operation_id
			WHERE t.campaign_id=$1 AND o.current_step='maintenance_stage' AND o.status='waiting')`, campaign.ID).Scan(&stage)
		if err != nil {
			t.Fatal(err)
		}
		if stage {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("persisted maintenance target never reached staging")
		case <-ticker.C:
		}
	}
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
