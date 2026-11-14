package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestControllerPrunesConfiguredHistory verifies that startup uses the
// configured ages for audit history and expired, dismissed notices.
func TestControllerPrunesConfiguredHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
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
	exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('00000000-0000-4000-8000-000000000001','active','Fixture','sqlite','https://management.test',
		'{workspace}.ide.test','{service}--{workspace}.preview.test','dev','dev','now','now',1)`)
	exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('member','member@example.test','member','active','local','setup','now','now',1)`)
	const timestamp = "2006-01-02T15:04:05.000000000Z"
	now := time.Now().UTC()
	published := now.AddDate(0, 0, -10).Format(timestamp)
	dismissed := now.AddDate(0, 0, -4).Format(timestamp)
	for _, event := range []struct {
		id string
		at time.Time
	}{
		{id: "old", at: now.AddDate(0, 0, -4)},
		{id: "recent", at: now.AddDate(0, 0, -2)},
	} {
		exec(`INSERT INTO audit_events(occurred_at,actor_type,actor_id,action,resource_type,resource_id,outcome,reason_code,metadata_json)
			VALUES($1,'user','member','workspace.inspect','workspace',$2,'allowed','owner','{}')`, event.at.Format(time.RFC3339Nano), event.id)
	}
	for _, notice := range []struct {
		id      string
		expires time.Time
	}{
		{id: "expired", expires: now.Add(-time.Hour)},
		{id: "active", expires: now.Add(time.Hour)},
	} {
		exec(`INSERT INTO notices(id,severity,title,body,effective_at,expires_at,created_by,created_at)
			VALUES($1,'info','Announcement','Details',$2,$3,'member',$2)`, notice.id, published, notice.expires.Format(timestamp))
		exec(`INSERT INTO notice_receipts(notice_id,user_id,read_at,dismissed_at)
			VALUES($1,'member',$2,$3)`, notice.id, published, dismissed)
	}

	days := 2
	auditDays := 3
	finished := make(chan error, 1)
	go func() {
		finished <- RunHistoryRetention(ctx, db, config.Config{
			DismissedNoticeRetentionDays: &days,
			AuditRetentionDays:           &auditDays,
		}, &backup.WriteFence{})
	}()
	defer func() {
		cancel()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Errorf("history retention shutdown = %v, want cancellation", err)
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired, active, oldAudit, recentAudit bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notice_receipts WHERE notice_id='expired'),
			EXISTS(SELECT 1 FROM notice_receipts WHERE notice_id='active'),
			EXISTS(SELECT 1 FROM audit_events WHERE resource_id='old'),
			EXISTS(SELECT 1 FROM audit_events WHERE resource_id='recent')`).Scan(&expired, &active, &oldAudit, &recentAudit)
		if err != nil {
			t.Fatalf("reading startup notice cleanup result: %v", err)
		}
		if !active {
			t.Fatal("startup cleanup removed the dismissal receipt for a current notice")
		}
		if !recentAudit {
			t.Fatal("startup cleanup removed an audit event within the configured window")
		}
		if !expired && !oldAudit {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("startup cleanup did not apply the configured notice and audit windows")
		case <-ticker.C:
		}
	}
}
