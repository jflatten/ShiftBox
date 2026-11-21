//go:build utm_release

package release_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestUTMRevokedGuestStaysIneligible verifies poll, restart and deletion
// behavior using the same revoked certificate and actual retained VM.
func TestUTMRevokedGuestStaysIneligible(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_REVOKED_GUEST") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_REVOKED_GUEST=1 for native revocation and deletion")
	}
	runUTMGuestFixture(t, nil, nativeRevokedGuest)
}

func verifyNativeRevokedGuest(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, before provider.Inventory, provisionID, owner string, ca *guest.Authority,
) {
	t.Helper()
	ref := before.Ref
	leaf := nativeEnrollmentLeaf(t, ctx, db, ref)
	enrollment := guest.NewEnrollment(db, ca)
	if contact, err := enrollment.Authenticate(ctx, leaf, time.Now()); err != nil || contact.WorkspaceID != ref.WorkspaceID {
		t.Fatalf("enrolled native certificate = %v, want authenticated workspace", err)
	}
	// Inject the revoked registration in the disposable controller database.
	// Capture the timestamp in the same write so an in-flight poll cannot
	// race the baseline for the refusal check.
	var lastPoll string
	if err := db.QueryRowContext(ctx, `UPDATE guest_registrations SET state='revoked'
		WHERE workspace_id=$1 AND generation=$2 AND state='active' RETURNING last_authenticated_at`,
		ref.WorkspaceID, ref.Generation).Scan(&lastPoll); err != nil {
		t.Fatal(err)
	}
	assertDenied := func(state string) {
		t.Helper()
		if _, err := enrollment.AuthenticatePoll(ctx, leaf, time.Now()); !errors.Is(err, guest.ErrGuestUnauthorized) {
			t.Fatalf("poll with the %s native registration = %v, want identity denial", state, err)
		}
	}
	assertDenied("revoked")
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	var afterPoll string
	if err := db.QueryRowContext(ctx, `SELECT last_authenticated_at FROM guest_registrations
		WHERE workspace_id=$1 AND generation=$2 AND state='revoked'`, ref.WorkspaceID, ref.Generation).Scan(&afterPoll); err != nil || afterPoll != lastPoll {
		t.Fatalf("native polling after revocation advanced contact or changed registration: %v", err)
	}
	running, err := machines.Inspect(ctx, ref)
	if err != nil || running.Power != provider.Running || running.Ref != ref {
		t.Fatalf("native VM after revocation = %s/%+v (%v), want the same running VM", running.Power, running.Ref, err)
	}
	verifyOwnedDisk(t, before, running)
	t.Log("revoked native certificate could not authenticate during 25 seconds of continued guest runtime")

	// The guest cannot acknowledge shutdown once revoked. Use the native host
	// stop, then reconcile it through the ordinary recorded lifecycle action.
	if _, err := machines.Stop(ctx, provider.StopRequest{Ref: ref, OperationID: "revoked-fixture-host-stop", ForceRetentionDelete: true}); err != nil {
		t.Fatal(err)
	}
	stopped := stopUTMEnrolledFixture(t, ctx, db, journal, machines, before, provisionID, owner)
	admit := func(action string) operation.Operation {
		t.Helper()
		var op operation.Operation
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			op, err = admitUTMFixturePower(ctx, tx, journal, ref.WorkspaceID, owner, action, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return op
	}
	start := admit("start")
	failure := waitNativeOperation(t, ctx, journal, start.ID, "failed")
	if !strings.Contains(failure.Steps[0].Detail, "ineligible for automatic recovery") {
		t.Fatalf("revoked start detail = %q, want actionable recovery refusal", failure.Steps[0].Detail)
	}
	var grants int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM enrollment_grants WHERE operation_id=$1", start.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("revoked start issued %d enrollment grants (%v), want 0", grants, err)
	}
	afterStart, err := machines.Inspect(ctx, ref)
	if err != nil || afterStart.Power != provider.Stopped || afterStart.Ref != ref {
		t.Fatalf("native VM after refused start = %s/%+v (%v), want the same stopped VM", afterStart.Power, afterStart.Ref, err)
	}
	verifyOwnedDisk(t, stopped, afterStart)

	deletion := admit("delete")
	waitNativeOperation(t, ctx, journal, deletion.ID, "succeeded")
	var state, disk string
	if err := db.QueryRowContext(ctx, `SELECT g.state, r.disk_state FROM guest_registrations g JOIN reservations r ON r.workspace_id=g.workspace_id
		WHERE g.workspace_id=$1 AND g.generation=$2`, ref.WorkspaceID, ref.Generation).Scan(&state, &disk); err != nil || state != "deleted" || disk != "released" {
		t.Fatalf("after confirmed deletion: registration %q, disk reservation %q (%v), want deleted and released", state, disk, err)
	}
	if _, err := machines.Inspect(ctx, ref); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("inspect deleted native VM = %v, want provider absence", err)
	}
	assertDenied("deleted")
	t.Log("revoked start issued no grant and preserved the stopped disk; verified deletion released it and permanently denied the old certificate")
}

func stopUTMEnrolledFixture(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, before provider.Inventory, provisionID, owner string,
) provider.Inventory {
	t.Helper()
	var stop operation.Operation
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		now := time.Now()
		// This fixture exercises the enrolled management agent without checkout.
		if err := journal.CancelInTx(ctx, tx, provisionID, "Cancelled by release test.", now); err != nil {
			return err
		}
		var err error
		stop, err = admitUTMFixturePower(ctx, tx, journal, before.Ref.WorkspaceID, owner, "stop", now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	waitNativeOperation(t, ctx, journal, stop.ID, "succeeded")
	stopped, err := machines.Inspect(ctx, before.Ref)
	if err != nil || stopped.Power != provider.Stopped || stopped.Ref != before.Ref {
		t.Fatalf("UTM after authorized stop = %s/%+v (%v), want stopped/%+v", stopped.Power, stopped.Ref, err, before.Ref)
	}
	verifyOwnedDisk(t, before, stopped)
	return stopped
}

func admitUTMFixturePower(ctx context.Context, tx *sql.Tx, journal *operation.Journal, workspaceID, owner, action string, now time.Time) (operation.Operation, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", workspaceID).Scan(&version); err != nil {
		return operation.Operation{}, err
	}
	return workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{WorkspaceID: workspaceID,
		ActorID: owner, RequestKey: "native-workspace-" + action, Action: action, Version: version}, now)
}

func waitForNativeGuestExpiry(t *testing.T, ctx context.Context, db *sql.DB, ref provider.WorkspaceRef) *x509.Certificate {
	t.Helper()
	leaf := nativeEnrollmentLeaf(t, ctx, db, ref)
	if !time.Now().Before(leaf.NotAfter) {
		t.Fatal("fixture leaf expired before the controller verified the native stop")
	}
	t.Logf("waiting for the stopped guest certificate to expire in %s", time.Until(leaf.NotAfter).Round(time.Second))
	timer := time.NewTimer(time.Until(leaf.NotAfter) + 2*time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	return leaf
}

func nativeEnrollmentLeaf(t *testing.T, ctx context.Context, db *sql.DB, ref provider.WorkspaceRef) *x509.Certificate {
	t.Helper()
	var encoded string
	if err := db.QueryRowContext(ctx, `SELECT certificate_der FROM enrollment_grants WHERE workspace_id=$1
		AND generation=$2 AND state='consumed'`, ref.WorkspaceID, ref.Generation).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	der, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func verifyNativeGuestRecovery(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, stopped provider.Inventory, owner string, prior *x509.Certificate,
) {
	t.Helper()
	ref := stopped.Ref
	startedAt := time.Now().UTC()
	var start operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		start, err = admitUTMFixturePower(ctx, tx, journal, ref.WorkspaceID, owner, "start", startedAt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		op, err := journal.Get(ctx, start.ID)
		if err != nil {
			t.Fatal(err)
		}
		if op.Steps[0].Status == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native start never verified fresh guest contact: %+v", op.Steps[0])
		case <-ticker.C:
		}
	}
	var encoded, invalidated, contactStamp string
	var cleared bool
	err := db.QueryRowContext(ctx, `SELECT fresh.certificate_der,prior.state,r.last_authenticated_at,
		fresh.media_directory IS NULL AND fresh.media_attached_at IS NULL
		FROM enrollment_grants fresh JOIN enrollment_grants prior ON prior.workspace_id=fresh.workspace_id AND prior.generation=fresh.generation
		JOIN guest_registrations r ON r.workspace_id=fresh.workspace_id AND r.generation=fresh.generation
		WHERE fresh.operation_id=$1 AND fresh.state='consumed' AND fresh.provider_instance_id=$2
		AND prior.operation_id<>fresh.operation_id AND r.state='active' AND r.public_key_fingerprint=fresh.key_proof`,
		start.ID, ref.InstanceID).Scan(&encoded, &invalidated, &contactStamp, &cleared)
	if err != nil || invalidated != "invalidated" || !cleared {
		t.Fatalf("native recovery grant = %s, media clearance = %t (%v), want invalidated predecessor and cleared media", invalidated, cleared, err)
	}
	contact, err := time.Parse(time.RFC3339Nano, contactStamp)
	if err != nil || contact.Before(startedAt) {
		t.Fatalf("native guest contact = %v (%v), want contact after fresh start", contact, err)
	}
	der, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil || leaf.SerialNumber.Cmp(prior.SerialNumber) == 0 || !leaf.NotAfter.After(startedAt) {
		t.Fatalf("native recovery leaf did not replace the expired identity: %v", err)
	}
	if bytes.Equal(leaf.RawSubjectPublicKeyInfo, prior.RawSubjectPublicKeyInfo) {
		t.Fatal("native recovery reused the expired guest key")
	}
	resumed, err := machines.Inspect(ctx, ref)
	if err != nil || resumed.Power != provider.Running || resumed.Ref != ref {
		t.Fatalf("recovered UTM workspace = %s/%+v (%v), want running/%+v", resumed.Power, resumed.Ref, err, ref)
	}
	verifyOwnedDisk(t, stopped, resumed)
	t.Log("native guest authenticated with a replacement identity on the same VM and disk; enrollment media was cleared")
}
