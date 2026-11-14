package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// TestPruneDismissedNotices preserves read-only history and dismissal of notices
// still being shown to other users. Recovery state also suspends pruning.
func TestPruneDismissedNotices(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "notices.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('00000000-0000-4000-8000-000000000001','recovery','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','dev','dev','now','now',1)`)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('admin','admin@example.test','administrator','active','local','setup','now','now',1)`)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -30)
	const stamp = "2006-01-02T15:04:05.000000000Z"
	for _, fixture := range []struct {
		id        string
		expires   any
		dismissed any
	}{
		{"old", now.Add(-time.Hour).Format(stamp), cutoff.Add(-time.Nanosecond).Format(stamp)},
		{"boundary", now.Format(stamp), cutoff.Format(stamp)},
		{"unexpired", now.Add(time.Hour).Format(stamp), cutoff.Add(-time.Hour).Format(stamp)},
		{"no-expiry", nil, cutoff.Add(-time.Hour).Format(stamp)},
		{"read-only", now.Add(-time.Hour).Format(stamp), nil},
	} {
		start := cutoff.AddDate(0, 0, -1).Format(stamp)
		exec(`INSERT INTO notices(id,severity,title,body,effective_at,expires_at,created_by,created_at)
			VALUES($1,'info','Notice','Details',$2,$3,'admin',$2)`, fixture.id, start, fixture.expires)
		exec(`INSERT INTO notice_receipts(notice_id,user_id,read_at,dismissed_at)
			VALUES($1,'admin',$2,$3)`, fixture.id, start, fixture.dismissed)
	}
	prune := func(want int64) {
		t.Helper()
		got, err := PruneDismissedNotices(ctx, db, now, cutoff)
		if err != nil || got != want {
			t.Fatalf("PruneDismissedNotices(%v, %v) = %d, %v, want %d", now, cutoff, got, err, want)
		}
	}
	prune(0)
	exec("UPDATE installations SET state='active' WHERE singleton=1")
	prune(1)
	prune(0)
	var notices, receipts, oldReceipts int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM notices),
		(SELECT count(*) FROM notice_receipts),
		(SELECT count(*) FROM notice_receipts WHERE notice_id='old')`).Scan(&notices, &receipts, &oldReceipts); err != nil {
		t.Fatal(err)
	}
	if notices != 5 || receipts != 4 || oldReceipts != 0 {
		t.Errorf("after prune = %d notices, %d receipts, %d old receipts, want 5/4/0", notices, receipts, oldReceipts)
	}
}
