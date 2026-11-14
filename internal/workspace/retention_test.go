package workspace

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestRetentionCandidates verifies that an outage does not reset inactivity,
// but uncertain inventory and newly active workspaces remain ineligible.
func TestRetentionCandidates(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"due", "disabled", "fresh", "uncertain", "active-again"} {
		var seconds *int64
		duration := int64(3600)
		if id != "disabled" {
			seconds = &duration
		}
		created := now.Add(-2 * time.Hour)
		if id == "fresh" {
			created = now.Add(-time.Minute)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
			generation,ownership_marker,provider_instance_id,created_at,updated_at,version,retention_seconds)
			VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
			'devcontainer.json',2,1024,40,'{"mode":"shared"}','stopped','stopped',1,'owned',$3,$4,$4,1,$5)`,
			id, testInstallation, id+"-vm", created.Format(time.RFC3339Nano), seconds); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM workspaces"); err != nil {
			t.Error(err)
		}
	})
	var inspected int
	inspect := func(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
		inspected++
		switch ref.WorkspaceID {
		case "uncertain":
			return provider.Inventory{}, provider.ErrOutcomeUnknown
		case "active-again":
			if _, err := db.ExecContext(ctx, "UPDATE workspaces SET last_activity_at=$1 WHERE id=$2", now.Format(time.RFC3339Nano), ref.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}
		return provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, ObservedAt: now, Power: provider.Stopped,
			ArtifactID: "reservation-artifact", Release: "reservation-release", NetworkMode: "shared"}, nil
	}
	s := NewStore(db)
	limit := int64(7200)
	for _, policy := range []RetentionEligibility{
		{MaximumSeconds: nil, ReconciledAt: now.Add(-time.Hour), OutageGrace: 10 * time.Minute},
		{MaximumSeconds: &limit, OutageGrace: 10 * time.Minute},
		{MaximumSeconds: &limit, ReconciledAt: now.Add(-9 * time.Minute), OutageGrace: 10 * time.Minute},
	} {
		list, err := s.RetentionCandidates(ctx, testInstallation, now, policy, inspect)
		if err != nil || len(list) != 0 || inspected != 0 {
			t.Fatalf("ineligible policy returned %d candidates and %d inspections (%v), want zero", len(list), inspected, err)
		}
	}
	policy := RetentionEligibility{MaximumSeconds: &limit, ReconciledAt: now.Add(-10 * time.Minute), OutageGrace: 10 * time.Minute}
	list, err := s.RetentionCandidates(ctx, testInstallation, now, policy, inspect)
	if err != nil || len(list) != 1 || list[0].Ref.WorkspaceID != "due" || inspected != 3 {
		t.Fatalf("reconciled candidates = %+v and %d inspections (%v), want due only and 3 inspections", list, inspected, err)
	}
	if !list[0].Deadline.Equal(now.Add(-time.Hour)) {
		t.Errorf("deadline after grace = %v, want original elapsed deadline", list[0].Deadline)
	}
	if _, err := db.Exec("UPDATE workspaces SET retention_seconds=NULL WHERE id='due'"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := recheckRetention(ctx, tx, list[0], now, policy); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("final policy recheck = %v, want no eligible workspace", err)
	}
}
