package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestMaintenanceUpdateWorker advances on durable matching receipts alone
// and blocks a rolled-back update even after the guest reports current.
func TestMaintenanceUpdateWorker(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "wrong_digest"} {
		t.Run(outcome, func(t *testing.T) {
			db, journal, now := maintenanceFixture(t, "retained")
			ctx := t.Context()
			maintenanceExec(t, db, `UPDATE workspaces SET observed_state='running',guest_release='test',protocol_version='1'`)
			deadline := now.Add(time.Hour)
			campaign, err := scheduleMaintenanceFixture(t, db, journal, "reservation-release", deadline, now)
			if err != nil {
				t.Fatal(err)
			}
			store := NewStore(db)
			if err := store.PrepareMaintenanceTargets(ctx, journal, testInstallation, campaign.ID, deadline); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := db.QueryRow(`SELECT operation_id FROM maintenance_targets WHERE campaign_id=$1`, campaign.ID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("pinned release"))
			load := func(_ context.Context, id string) (release.GuestUpdate, error) {
				if id != "test" {
					t.Errorf("loaded release %q, want test", id)
				}
				return release.GuestUpdate{Metadata: release.GuestUpdateMetadata{Release: id, Protocol: "1", Digest: digest}}, nil
			}
			worker := store.MaintenanceUpdateWorker(journal, testInstallation, "1", load)
			var waiting *operation.StepWaiting
			if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
				t.Fatalf("staging without a receipt = %v, want waiting", err)
			}
			reported := digest
			if outcome == "wrong_digest" {
				reported[0] ^= 1
			}
			maintenanceExec(t, db, `INSERT INTO guest_dispatches(operation_id,dispatch_version,status,update_digest)
				VALUES($1,1,'succeeded',$2)`, id, hex.EncodeToString(reported[:]))
			if err := worker.RunOnce(ctx, id); err != nil {
				t.Fatal(err)
			}
			if outcome != "wrong_digest" {
				if err := worker.RunOnce(ctx, id); !errors.As(err, &waiting) {
					t.Fatalf("activation without a receipt = %v, want waiting", err)
				}
				maintenanceExec(t, db, `UPDATE guest_dispatches SET activation_status=$1 WHERE operation_id=$2`, outcome, id)
				if err := worker.RunOnce(ctx, id); err != nil {
					t.Fatal(err)
				}
				if outcome == "succeeded" {
					if err := worker.RunOnce(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := "failed"
			if outcome == "succeeded" {
				want = "succeeded"
			}
			if op, err := journal.Get(ctx, id); err != nil || op.Status != want {
				t.Errorf("update operation = %s (%v), want %s", op.Status, err, want)
			}
		})
	}
}
