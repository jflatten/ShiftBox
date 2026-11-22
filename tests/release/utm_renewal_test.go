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
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
)

// TestUTMGuestRenewsBeforeExpiry verifies that the packaged agent rotates its
// key and resumes authenticated contact before its first leaf expires.
func TestUTMGuestRenewsBeforeExpiry(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_NATIVE_RENEWAL") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_NATIVE_RENEWAL=1 for native certificate renewal")
	}
	runUTMGuestFixture(t, nil, nativeGuestRenewal)
}

func verifyNativeGuestRenewal(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider,
	before provider.Inventory, prior *x509.Certificate, ca *guest.Authority,
) {
	t.Helper()
	ref := before.Ref
	deadline, cancel := context.WithDeadline(ctx, prior.NotAfter)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var replacement *x509.Certificate
	var lastContact time.Time
	for {
		var receipt, predecessor sql.NullString
		var currentSerial, contactStamp string
		if err := db.QueryRowContext(deadline, `SELECT certificate_serial,last_authenticated_at,renewal_predecessor_serial,renewal_certificate_der
			FROM guest_registrations WHERE workspace_id=$1 AND generation=$2 AND state='active'`, ref.WorkspaceID, ref.Generation).
			Scan(&currentSerial, &contactStamp, &predecessor, &receipt); err != nil {
			t.Fatalf("read native renewal before the initial certificate expired: %v", err)
		}
		if receipt.Valid {
			der, err := hex.DecodeString(receipt.String)
			if err != nil {
				t.Fatal(err)
			}
			replacement, err = x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			if !predecessor.Valid || predecessor.String != prior.SerialNumber.Text(16) ||
				currentSerial != replacement.SerialNumber.Text(16) || currentSerial == predecessor.String {
				t.Fatal("renewal did not replace the exact prior certificate")
			}
			lastContact, err = time.Parse(time.RFC3339Nano, contactStamp)
			if err != nil {
				t.Fatal(err)
			}
			// Issuance rounds NotBefore to whole seconds. A later contact at
			// least one second after that can only come from the new leaf.
			if !lastContact.Before(replacement.NotBefore.Add(time.Second)) {
				break
			}
		}
		select {
		case <-deadline.Done():
			t.Fatal("the packaged guest did not authenticate under its replacement before the original certificate expired")
		case <-ticker.C:
		}
	}
	due := prior.NotBefore.Add(prior.NotAfter.Sub(prior.NotBefore) / 2)
	if replacement.NotBefore.Before(due.Truncate(time.Second)) || !lastContact.Before(prior.NotAfter) {
		t.Fatalf("renewal issued at %v and authenticated at %v, want a poll between the renewal window %v and expiry %v",
			replacement.NotBefore, lastContact, due, prior.NotAfter)
	}
	if bytes.Equal(prior.RawSubjectPublicKeyInfo, replacement.RawSubjectPublicKeyInfo) ||
		replacement.NotAfter.Sub(replacement.NotBefore) != 8*time.Hour {
		t.Fatal("the native renewal did not install a fresh key with the normal eight-hour lifetime")
	}
	if _, err := guest.NewEnrollment(db, ca).Authenticate(ctx, prior, lastContact); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Fatalf("ordinary authentication with the still-unexpired predecessor = %v, want identity denial", err)
	}
	var enrollments int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM enrollment_grants WHERE workspace_id=$1 AND generation=$2",
		ref.WorkspaceID, ref.Generation).Scan(&enrollments); err != nil || enrollments != 1 {
		t.Fatalf("native renewal left %d enrollment grants (%v), want the original grant only", enrollments, err)
	}
	after, err := machines.Inspect(ctx, ref)
	if err != nil || after.Power != provider.Running || after.Ref != ref {
		t.Fatalf("VM after renewal = %s/%+v (%v), want the same running guest", after.Power, after.Ref, err)
	}
	verifyOwnedDisk(t, before, after)
	t.Log("packaged agent rotated its key, used the new eight-hour certificate before the original expired, and retained the VM and disk")
}
