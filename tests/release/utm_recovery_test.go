//go:build utm_release

package release_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

func stopUTMEnrolledFixture(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, before provider.Inventory, provisionID, owner string,
) provider.Inventory {
	t.Helper()
	var stop operation.Operation
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		now := time.Now()
		// This fixture exercises the enrolled management agent without checkout.
		if err := journal.CancelInTx(ctx, tx, provisionID, now); err != nil {
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
