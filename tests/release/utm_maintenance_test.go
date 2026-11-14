//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestUTMGuestMaintenance verifies a signed trial fails and rolls back on the
// real guest, then a signed replacement settles the stopped workspace and
// clears the retained-guest gate. Both inputs use the fixture publisher key.
func TestUTMGuestMaintenance(t *testing.T) {
	failing := os.Getenv("SHIFTBOX_TEST_UTM_ROLLBACK_RELEASES")
	working := os.Getenv("SHIFTBOX_TEST_UTM_UPDATE_RELEASES")
	if failing == "" && working == "" {
		t.Skip("set both signed native maintenance fixture release directories")
	}
	if !filepath.IsAbs(failing) || !filepath.IsAbs(working) || failing == working {
		t.Fatal("set distinct absolute rollback and update release directories")
	}
	runUTMGuestFixture(t, &nativeMaintenanceFixture{failing: failing, working: working})
}

type nativeMaintenanceFixture struct {
	failing, working string
	retry            atomic.Bool
	protocol         string
	rollbackDigest   string
	updateDigest     string
}

func (f *nativeMaintenanceFixture) prepare(t *testing.T, ctx context.Context, metadata release.ArtifactManifest) {
	t.Helper()
	f.protocol = metadata.ProtocolVersion
	for _, input := range []struct {
		root   string
		digest *string
	}{{f.failing, &f.rollbackDigest}, {f.working, &f.updateDigest}} {
		update, err := release.ReadLocalGuestUpdate(ctx, input.root, metadata.Guest.Version, f.protocol)
		if err != nil {
			t.Fatalf("read signed update for %s in %s: %v", metadata.Guest.Version, input.root, err)
		}
		*input.digest = hex.EncodeToString(update.Metadata.Digest[:])
	}
	if f.rollbackDigest == f.updateDigest || f.rollbackDigest == metadata.Guest.SHA256 || f.updateDigest == metadata.Guest.SHA256 {
		t.Fatal("native trials require distinct bytes from each other and the bundled agent")
	}
}

func (f *nativeMaintenanceFixture) load(ctx context.Context, releaseID string) (release.GuestUpdate, error) {
	root := f.failing
	if f.retry.Load() {
		root = f.working
	}
	return release.ReadLocalGuestUpdate(ctx, root, releaseID, f.protocol)
}

func (f *nativeMaintenanceFixture) verify(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	machines *utm.Provider, before provider.Inventory, provisionID, owner string, metadata release.ArtifactManifest,
) {
	t.Helper()
	ref := before.Ref
	var stop operation.Operation
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		now := time.Now()
		// This fixture exercises the enrolled management agent without checkout.
		if err := journal.CancelInTx(ctx, tx, provisionID, now); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", ref.WorkspaceID).Scan(&version); err != nil {
			return err
		}
		var err error
		stop, err = workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{WorkspaceID: ref.WorkspaceID,
			ActorID: owner, RequestKey: "native-maintenance-stop", Action: "stop", Version: version}, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	waitNativeOperation(t, ctx, journal, stop.ID, "succeeded")
	for _, scenario := range []struct {
		key, campaign, operation, activation, digest string
		retry                                        bool
	}{
		{key: "native-maintenance-rollback", campaign: "blocked", operation: "failed", activation: "failed", digest: f.rollbackDigest},
		{key: "native-maintenance-replacement", campaign: "completed", operation: "succeeded", activation: "succeeded", digest: f.updateDigest, retry: true},
	} {
		f.retry.Store(scenario.retry)
		var campaign operation.Operation
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			now := time.Now()
			var err error
			campaign, err = workspace.ScheduleMaintenance(ctx, tx, journal, ref.InstallationID, owner, scenario.key,
				metadata.ReleaseID, now.Add(2*time.Second), now)
			return err
		}); err != nil {
			t.Fatalf("schedule %s: %v", scenario.key, err)
		}
		t.Logf("scheduled %s for the stopped UTM guest", scenario.key)
		waitNativeOperation(t, ctx, journal, campaign.ID, scenario.operation)
		var state, targetID, stagedDigest, activation string
		if err := db.QueryRowContext(ctx, `SELECT c.state,t.operation_id,g.update_digest,g.activation_status
			FROM maintenance_campaigns c JOIN maintenance_targets t ON t.campaign_id=c.operation_id
			JOIN guest_dispatches g ON g.operation_id=t.operation_id WHERE c.operation_id=$1 AND t.workspace_id=$2`,
			campaign.ID, ref.WorkspaceID).Scan(&state, &targetID, &stagedDigest, &activation); err != nil {
			t.Fatal(err)
		}
		if state != scenario.campaign || stagedDigest != scenario.digest || activation != scenario.activation {
			t.Fatalf("%s campaign = %s/%s/%s, want %s/%s/%s", scenario.key, state, stagedDigest, activation, scenario.campaign, scenario.digest, scenario.activation)
		}
		target := waitNativeOperation(t, ctx, journal, targetID, scenario.operation)
		if len(target.Steps) != 5 || target.Steps[0].Status != "succeeded" || target.Steps[1].Status != "succeeded" ||
			target.Steps[2].Status != scenario.activation || target.Steps[4].Status != "succeeded" {
			t.Fatalf("%s unexpected maintenance steps: %+v", scenario.key, target.Steps)
		}
		if scenario.retry && target.Steps[3].Status != "succeeded" ||
			!scenario.retry && (target.Steps[3].Status != "skipped" || !strings.Contains(target.Steps[2].Detail, "rolled back")) {
			t.Fatalf("%s unexpected verification evidence: %+v", scenario.key, target.Steps)
		}
		after, err := machines.Inspect(ctx, ref)
		if err != nil || after.Power != provider.Stopped || after.Ref != ref {
			t.Fatalf("%s provider state = %s/%+v (%v), want stopped/%+v", scenario.key, after.Power, after.Ref, err, ref)
		}
		verifyOwnedDisk(t, before, after)
		var desired, observed, compute, disk string
		if err := db.QueryRowContext(ctx, `SELECT w.desired_state,w.observed_state,r.cpu_memory_state,r.disk_state
			FROM workspaces w JOIN reservations r ON r.workspace_id=w.id WHERE w.id=$1`, ref.WorkspaceID).
			Scan(&desired, &observed, &compute, &disk); err != nil || desired != "stopped" || observed != "stopped" || compute != "released" || disk != "reserved" {
			t.Fatalf("%s state and reservations = %s/%s, %s/%s (%v), want stopped/stopped, released/reserved", scenario.key, desired, observed, compute, disk, err)
		}
		blockers, err := release.NewStore(db).GuestUpgradeBlockers(ctx, ref.InstallationID, metadata.Guest.Version, metadata.ProtocolVersion)
		if err != nil {
			t.Fatal(err)
		}
		if scenario.retry && len(blockers) != 0 || !scenario.retry && (len(blockers) != 1 || blockers[0].WorkspaceID != ref.WorkspaceID || blockers[0].ObservedState != "stopped") {
			t.Fatalf("%s unexpected retained guest blockers: %+v", scenario.key, blockers)
		}
		t.Logf("%s: signed digest %s, activation %s, restored stopped state, %d retained guest blockers", scenario.key, stagedDigest, activation, len(blockers))
	}
}

func waitNativeOperation(t *testing.T, ctx context.Context, journal *operation.Journal, id, status string) operation.Operation {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		op, err := journal.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == status {
			return op
		}
		if op.FinishedAt != nil {
			t.Fatalf("operation %s = %s, want %s, steps %+v", id, op.Status, status, op.Steps)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("operation %s timed out: %s, want %s, steps %+v", id, op.Status, status, op.Steps)
		case <-ticker.C:
		}
	}
}
