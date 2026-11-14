package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
)

// TestHistoryRetentionSchedule verifies cleanup starts immediately, continues
// after a failed attempt, and finishes when the controller stops.
func TestHistoryRetentionSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var attempts atomic.Int32
		var failures atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- runHistoryRetention(ctx, 24*time.Hour, func(context.Context) error {
				if attempts.Add(1) == 1 {
					return errors.New("temporary cleanup failure")
				}
				return nil
			}, func(error) { failures.Add(1) })
		}()
		synctest.Wait()
		if got := attempts.Load(); got != 1 {
			t.Errorf("attempts at controller start = %d, want 1", got)
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if got := attempts.Load(); got != 2 {
			t.Errorf("attempts after one day = %d, want 2", got)
		}
		if got := failures.Load(); got != 1 {
			t.Errorf("reported failures = %d, want 1", got)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("runHistoryRetention on shutdown = %v, want context.Canceled", err)
		}
	})
}

// TestPruneDismissedNotices preserves read-only history and dismissal of notices
// still being shown to other users. Recovery state also suspends pruning.
func TestPruneDismissedNotices(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		testPruneDismissedNotices(t, database.Config{
			Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "notices.db"),
		})
	})
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if uri == "" {
			t.Skip("PostgreSQL fixture not configured")
		}
		testPruneDismissedNotices(t, database.Config{Profile: "postgres", Schema: "shiftbox_test", PostgresURI: uri})
	})
}

func testPruneDismissedNotices(t *testing.T, cfg database.Config) {
	t.Helper()
	ctx := t.Context()
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.Profile == "postgres" {
		// Shadow durable tables on the fixture's single connection.
		for _, name := range []string{"00001_foundation.sql", "00011_notices.sql"} {
			ddl, err := migrations.Files.ReadFile("postgres/" + name)
			if err != nil {
				t.Fatal(err)
			}
			up, _, _ := strings.Cut(string(ddl), "-- +goose Down")
			exec(strings.ReplaceAll(up, "CREATE TABLE ", "CREATE TEMPORARY TABLE "))
		}
	} else if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('00000000-0000-4000-8000-000000000001','recovery','ShiftBox',$1,'https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','dev','dev','now','now',1)`, cfg.Profile)
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
