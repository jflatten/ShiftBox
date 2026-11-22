package workspace

import (
	"database/sql"
	"testing"
	"time"
)

// TestSSHActivityAdvancesInactivity verifies a real SSH event renews both
// deadlines and that idle polls, replay, or invalid workspace state cannot renew them.
func TestSSHActivityAdvancesInactivity(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			db := openWorkspaceDatabase(t, profile)
			now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
			const id = "ssh-activity"
			seedInactivityWorkspaces(t, db, now.Add(-time.Hour), id)
			inactivityExec(t, db, "UPDATE workspaces SET retention_seconds=600 WHERE id=$1", id)
			store := NewStore(db)
			if due, err := store.InactivityCandidates(t.Context(), testInstallation, now); err != nil || len(due) != 1 || due[0] != id {
				t.Fatalf("inactivity before SSH activity = %v (%v), want [%s]", due, err, id)
			}
			record := func(generation int64, received, event time.Time) {
				t.Helper()
				if err := RecordSSHActivity(t.Context(), db, id, generation, received, &event); err != nil {
					t.Fatal(err)
				}
			}
			check := func(want time.Time) {
				t.Helper()
				var activity, deadline sql.NullString
				if err := db.QueryRow("SELECT last_activity_at,retention_deadline FROM workspaces WHERE id=$1", id).Scan(&activity, &deadline); err != nil {
					t.Fatal(err)
				}
				if want.IsZero() {
					if activity.Valid {
						t.Errorf("SSH activity = %v, want unset", activity)
					}
					return
				}
				if activity.String != want.Format(time.RFC3339Nano) || deadline.String != want.Add(10*time.Minute).Format(time.RFC3339Nano) {
					t.Errorf("SSH activity and deadline = %v/%v, want %v and ten minutes later", activity, deadline, want)
				}
			}
			record(2, now, now)
			record(1, now, now.Add(time.Second))
			record(1, now, now.Add(-91*time.Second))
			check(time.Time{})
			event := now.Add(-15 * time.Second)
			record(1, now, event)
			record(1, now.Add(10*time.Second), event)
			check(event)
			if due, err := store.InactivityCandidates(t.Context(), testInstallation, now); err != nil || len(due) != 0 {
				t.Errorf("inactivity after SSH activity = %v (%v), want no due workspace", due, err)
			}
			inactivityExec(t, db, "UPDATE workspaces SET desired_state='stopped' WHERE id=$1", id)
			record(1, now, now)
			check(event)
		})
	}
}

// TestHumanAndProcessActivity keeps the clock tied to actual input or a fresh
// explicit watch report; stale samples, stopped VMs and old generations cannot renew it.
func TestHumanAndProcessActivity(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			db := openWorkspaceDatabase(t, profile)
			now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
			const id = "human-activity"
			seedInactivityWorkspaces(t, db, now.Add(-time.Hour), id)
			check := func(want time.Time) {
				t.Helper()
				var activity sql.NullString
				if err := db.QueryRow("SELECT last_activity_at FROM workspaces WHERE id=$1", id).Scan(&activity); err != nil {
					t.Fatal(err)
				}
				if want.IsZero() && activity.Valid || !want.IsZero() && activity.String != want.Format(time.RFC3339Nano) {
					t.Errorf("activity=%v, want %v", activity, want)
				}
			}
			for _, sample := range []*ProcessActivity{nil, {SampledAt: now, Active: false},
				{SampledAt: now.Add(-91 * time.Second), Active: true}, {SampledAt: now.Add(time.Second), Active: true}} {
				if err := RecordProcessActivity(t.Context(), db, id, 1, now, sample); err != nil {
					t.Fatal(err)
				}
			}
			check(time.Time{})
			if err := RecordHumanActivity(t.Context(), db, id, 2, now); err != nil {
				t.Fatal(err)
			}
			check(time.Time{})
			if err := RecordHumanActivity(t.Context(), db, id, 1, now); err != nil {
				t.Fatal(err)
			}
			check(now)
			later := now.Add(time.Minute)
			if err := RecordProcessActivity(t.Context(), db, id, 1, later, &ProcessActivity{SampledAt: later, Active: true}); err != nil {
				t.Fatal(err)
			}
			check(later)
			if err := RecordHumanActivity(t.Context(), db, id, 1, now); err != nil {
				t.Fatal(err)
			}
			check(later)
			inactivityExec(t, db, "UPDATE workspaces SET desired_state='stopped' WHERE id=$1", id)
			if err := RecordHumanActivity(t.Context(), db, id, 1, later.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			check(later)
		})
	}
}
