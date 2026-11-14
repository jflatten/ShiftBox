package workspace

import (
	"database/sql"
	"testing"
	"time"
)

// TestRecordActivity keeps idle, stale, and wrong-generation reports from
// extending retention, even when a guest continues polling.
func TestRecordActivity(t *testing.T) {
	ctx := t.Context()
	db := openWorkspaceDatabase(t, "sqlite")
	seedReservationWorkspaces(t, db)
	const id = "activity-workspace"
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM workspaces WHERE id=$1", id); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,
		platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,
		desired_state,observed_state,generation,ownership_marker,created_at,updated_at,version,retention_seconds)
		VALUES($1,$2,'reservation-owner','template',1,'reservation-artifact','https://github.com/example/project',
		'devcontainer.json',2,1024,40,'{"mode":"shared"}','running','running',1,'owned',$3,$3,1,600)`,
		id, testInstallation, testStamp); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	policy := ActivityPolicy{IDE: true, CPU: true, CPUThreshold: 5, Freshness: 90 * time.Second}
	record := func(generation int64, received time.Time, sample *CPUActivity, ide bool) {
		t.Helper()
		if err := RecordActivity(ctx, db, id, generation, received, policy, sample, ide); err != nil {
			t.Fatal(err)
		}
	}
	assert := func(want time.Time) {
		t.Helper()
		var activity, deadline sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT last_activity_at,retention_deadline FROM workspaces WHERE id=$1", id).Scan(&activity, &deadline); err != nil {
			t.Fatal(err)
		}
		if want.IsZero() {
			if activity.Valid || deadline.Valid {
				t.Fatal("idle or stale activity set a deadline")
			}
			return
		}
		got, e1 := time.Parse(time.RFC3339Nano, activity.String)
		due, e2 := time.Parse(time.RFC3339Nano, deadline.String)
		if e1 != nil || e2 != nil || !got.Equal(want) || !due.Equal(want.Add(10*time.Minute)) {
			t.Errorf("activity = %v, deadline = %v, want %v and ten minutes later", got, due, want)
		}
	}
	record(1, now, &CPUActivity{SampledAt: now, Percent: 5}, false)
	record(1, now, &CPUActivity{SampledAt: now.Add(-91 * time.Second), Percent: 80}, false)
	record(2, now, nil, true)
	assert(time.Time{})
	record(1, now, &CPUActivity{SampledAt: now.Add(-time.Second), Percent: 6}, false)
	assert(now)
	record(1, now.Add(time.Minute), &CPUActivity{SampledAt: now.Add(time.Minute), Percent: 0}, false)
	assert(now)
	record(1, now.Add(2*time.Minute), nil, true)
	record(1, now.Add(time.Minute), nil, true)
	assert(now.Add(2 * time.Minute))
}
