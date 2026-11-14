package app

import (
	"context"
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
	"github.com/jflatten/ShiftBox/internal/web"
)

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
