package workspace

import (
	"database/sql"
	"testing"
	"time"
)

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
