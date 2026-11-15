//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
)

// TestUTMClonedEnrollmentMedia starts a fresh UTM clone with the original
// guest's consumed enrollment image and checks that it obtains no identity.
func TestUTMClonedEnrollmentMedia(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_CLONED_ENROLLMENT") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_CLONED_ENROLLMENT=1 for native enrollment-media replay")
	}
	runUTMGuestFixture(t, nil, nativeClonedEnrollment)
}

type nativeEnrollmentCapture struct {
	provider.Provider
	image string
}

// DeliverEnrollment copies the fixture image while it still contains the
// grant, before the normal controller clears the original after first use.
func (p nativeEnrollmentCapture) DeliverEnrollment(ctx context.Context, request provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	image, err := os.ReadFile(request.MediaPath)
	if err != nil {
		return provider.DeliveryResult{}, err
	}
	defer clear(image)
	if err := os.WriteFile(p.image, image, 0600); err != nil {
		return provider.DeliveryResult{}, err
	}
	return p.Provider.DeliverEnrollment(ctx, request)
}

func verifyNativeClonedEnrollment(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider,
	original provider.Inventory, image, host string,
) {
	t.Helper()
	ref := original.Ref
	var serial, key string
	var cleared bool
	err := db.QueryRowContext(ctx, `SELECT r.certificate_serial,r.public_key_fingerprint,
		g.media_directory IS NULL AND g.media_attached_at IS NULL
		FROM guest_registrations r JOIN enrollment_grants g
		ON g.workspace_id=r.workspace_id AND g.generation=r.generation AND g.key_proof=r.public_key_fingerprint
		WHERE r.workspace_id=$1 AND r.generation=$2 AND r.state='active' AND g.state='consumed'`,
		ref.WorkspaceID, ref.Generation).Scan(&serial, &key, &cleared)
	if err != nil || !cleared {
		t.Fatalf("original enrollment media clearance = %t (%v), want authenticated consumption and clearance", cleared, err)
	}
	seed, refusal := serveNativeEnrollmentObserver(t, host)
	cloneRef := provider.WorkspaceRef{InstallationID: ref.InstallationID, WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: uuid.NewString()}
	t.Cleanup(func() { cleanupEnrollmentVM(t, machines, cloneRef) })
	const operationID = "cloned-enrollment-fixture"
	clone, err := machines.Create(ctx, provider.CreateRequest{
		Ref: cloneRef, OperationID: operationID, ArtifactID: original.ArtifactID, Release: original.Release,
		NetworkMode: original.NetworkMode, Resources: original.Effective,
	})
	if err != nil || clone.Power != provider.Stopped || clone.Ref.InstanceID == ref.InstanceID {
		t.Fatalf("copied fixture provider instance = %s (%v), want a distinct stopped VM", clone.Power, err)
	}
	if _, err := machines.DeliverEnrollment(ctx, provider.EnrollmentRequest{
		Ref: clone.Ref, OperationID: operationID, MediaPath: image, Eligible: true,
	}); err != nil {
		t.Fatal(err)
	}
	starter := nativeSeedProvider{Provider: machines, seed: seed}
	if _, err := starter.Start(ctx, provider.StartRequest{
		Ref: clone.Ref, OperationID: operationID, NetworkMode: clone.NetworkMode, RequiresEnrollment: true, EnrollmentMedia: image,
	}); err != nil {
		t.Fatal(err)
	}

	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case <-refusal:
	case <-wait.Done():
		t.Fatal("the native clone did not report that enrollment was denied")
	}

	var afterSerial, afterKey string
	if err := db.QueryRowContext(ctx, `SELECT certificate_serial,public_key_fingerprint FROM guest_registrations
		WHERE workspace_id=$1 AND generation=$2 AND state='active'`, ref.WorkspaceID, ref.Generation).Scan(&afterSerial, &afterKey); err != nil ||
		afterSerial != serial || afterKey != key {
		t.Fatalf("original guest identity changed after enrollment media replay: %v", err)
	}
	var cloneIdentities int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM guest_registrations WHERE workspace_id=$1", clone.Ref.WorkspaceID).
		Scan(&cloneIdentities); err != nil || cloneIdentities != 0 {
		t.Fatalf("clone has %d registrations (%v), want none", cloneIdentities, err)
	}
	retained, err := machines.Inspect(ctx, ref)
	if err != nil || retained.Power != provider.Stopped || retained.Ref != ref {
		t.Fatalf("original after enrollment media replay = %s (%v), want the same stopped VM", retained.Power, err)
	}
	verifyOwnedDisk(t, original, retained)
	t.Log("native clone reported the packaged guest agent's enrollment refusal; original certificate and disk were retained")
}

func serveNativeEnrollmentObserver(t *testing.T, host string) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	path := "/" + id + "/"
	seed := "http://" + listener.Addr().String() + path
	refusal := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path+"meta-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "instance-id: %s\n", id)
	})
	mux.HandleFunc("GET "+path+"user-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `#!/bin/sh
set -eu
for attempt in $(seq 1 90); do
    if journalctl -b -u shiftbox-agent.service --quiet -o cat | grep -qx 'guest enrollment denied'; then
        test ! -e /var/lib/shiftbox-agent/identity.json
        curl -fsS --retry 3 --max-time 3 '%sdenied'
        exit
    fi
    sleep 1
done
exit 1
`, seed)
	})
	mux.HandleFunc("GET "+path+"denied", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case refusal <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("enrollment fixture seed server: %v", err)
		}
	})
	return seed, refusal
}
