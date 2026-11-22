//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
)

// TestUTMGuestRejectsExpiredCA lets the retained authority expire while the
// native guest is running, then checks that polling and CA loading fail closed.
func TestUTMGuestRejectsExpiredCA(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_EXPIRED_CA") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_EXPIRED_CA=1 for native CA expiry")
	}
	runUTMGuestFixture(t, nil, nativeExpiredCA)
}

func verifyNativeExpiredCA(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider, before provider.Inventory,
	authority *guest.Authority, cipher *cryptostore.Store, root string,
) {
	t.Helper()
	expiry := authority.Certificate.NotAfter
	if remaining := time.Until(expiry); remaining <= 0 || remaining > 4*time.Minute {
		t.Fatalf("fixture CA duration remaining = %v, want expiry within four minutes", remaining)
	}
	initial, _ := nativeGuestContact(t, ctx, db, before.Ref)
	t.Logf("waiting for the running guest's CA to expire in %s", time.Until(expiry).Round(time.Second))
	wait := func(delay time.Duration) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
	wait(time.Until(expiry) + 3*time.Second)
	last, serial := nativeGuestContact(t, ctx, db, before.Ref)
	if !last.After(initial) || !last.Before(expiry) {
		t.Fatalf("last authenticated contact = %v, want after %v and before CA expiry %v", last, initial, expiry)
	}
	wait(15 * time.Second)
	afterExpiry, afterSerial := nativeGuestContact(t, ctx, db, before.Ref)
	if !afterExpiry.Equal(last) || afterSerial != serial {
		t.Fatalf("the guest authenticated or renewed after its CA expired: contact %v, prior contact %v", afterExpiry, last)
	}
	if loaded, err := app.LoadInstallationCA(root, before.Ref.InstallationID, cipher, time.Now().UTC()); err == nil || loaded != nil {
		t.Fatal("controller loaded an expired installation authority")
	}
	vm, err := machines.Inspect(ctx, before.Ref)
	if err != nil || vm.Ref != before.Ref || vm.Power != provider.Running {
		t.Fatalf("VM after CA expiry = %s/%+v (%v), want the same running guest", vm.Power, vm.Ref, err)
	}
	verifyOwnedDisk(t, before, vm)
	t.Log("expired CA stopped authenticated contact and could not be reloaded; the VM and owned disk were retained")
}

// reserveNativeGuestAddresses chooses distinct endpoints for controller restarts.
func reserveNativeGuestAddresses(t *testing.T, bind string) [3]string {
	t.Helper()
	var addresses [3]string
	for i := range addresses {
		listener, err := net.Listen("tcp", bind)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		addresses[i] = listener.Addr().String()
	}
	return addresses
}

func verifyNativeControllerTrust(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider,
	before provider.Inventory, authority *guest.Authority, root string,
	restart func(*guest.Authority, func() time.Time) func(),
) {
	t.Helper()
	untrustedDir := filepath.Join(root, "other-trust")
	if err := os.Mkdir(untrustedDir, 0700); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	untrusted, err := app.EnsureInstallationCA(untrustedDir, uuid.NewString(), cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []struct {
		name      string
		authority *guest.Authority
		now       func() time.Time
	}{
		{"untrusted controller CA", untrusted, time.Now},
		{"future-dated server certificate", authority, func() time.Time { return time.Now().Add(time.Hour) }},
	} {
		priorContact, priorSerial := nativeGuestContact(t, ctx, db, before.Ref)
		func() {
			stop := restart(fault.authority, fault.now)
			defer stop()
			timer := time.NewTimer(20 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-timer.C:
			}
			got, serial := nativeGuestContact(t, ctx, db, before.Ref)
			if !got.Equal(priorContact) || serial != priorSerial {
				t.Fatalf("%s advanced authenticated contact from %v to %v or changed the client identity", fault.name, priorContact, got)
			}
		}()
		func() {
			stop := restart(authority, time.Now)
			defer stop()
			wait, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				got, serial := nativeGuestContact(t, wait, db, before.Ref)
				if serial != priorSerial {
					t.Fatalf("guest identity changed after restoring trust following %s", fault.name)
				}
				if got.After(priorContact) {
					break
				}
				select {
				case <-wait.Done():
					t.Fatalf("native guest did not resume authenticated polling after %s: %v", fault.name, wait.Err())
				case <-ticker.C:
				}
			}
		}()
		after, err := machines.Inspect(ctx, before.Ref)
		if err != nil || after.Power != provider.Running || after.Ref != before.Ref {
			t.Fatalf("native guest after %s = %s/%+v (%v), want running/%+v", fault.name, after.Power, after.Ref, err, before.Ref)
		}
		verifyOwnedDisk(t, before, after)
		t.Logf("unauthenticated during %s; original trust resumed polling on the same running VM", fault.name)
	}
}

func nativeGuestContact(t *testing.T, ctx context.Context, db *sql.DB, ref provider.WorkspaceRef) (time.Time, string) {
	t.Helper()
	var stamp, serial string
	if err := db.QueryRowContext(ctx, "SELECT last_authenticated_at,certificate_serial FROM guest_registrations WHERE workspace_id=$1 AND generation=$2 AND state='active'",
		ref.WorkspaceID, ref.Generation).Scan(&stamp, &serial); err != nil {
		t.Fatal(err)
	}
	contact, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return contact, serial
}
