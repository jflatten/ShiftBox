package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestPlatformImportAdmission binds an import to its original request and
// reopens the database before work and after an interrupted completion.
func TestPlatformImportAdmission(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	open := func() *sql.DB {
		t.Helper()
		db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "fixture.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	org := uuid.NewString()
	now := time.Now()
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite','https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','fixture','fixture','now','now',1)`, org); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
	var disconnect context.CancelFunc
	var ids []string
	importImage := func(_ context.Context, id, actor, folder string, makeDefault bool) error {
		ids = append(ids, id)
		if actor != "admin" || folder != "/test/image" || !makeDefault {
			t.Errorf("worker import = %q, %q, %t; want accepted fixture input", actor, folder, makeDefault)
		}
		if disconnect != nil {
			disconnect()
			return context.Canceled
		}
		return nil
	}
	proc := NewPlatformImport(db, journal, "https://management.test", org, func() time.Time { return now }, importImage, "utm")
	restart := func() {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		journal = operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
		proc = NewPlatformImport(db, journal, "https://management.test", org, func() time.Time { return now }, importImage, "utm")
	}
	actor := auth.Principal{User: auth.User{ID: "admin", Role: "administrator"}}
	submit := func(key, body string) (int, string, error) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/platform-artifacts", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		response, err := proc.Handler().Apply(ctx, tx, req, actor, web.AuthorizationTarget{}, auth.Decision{})
		if err != nil {
			return 0, "", err
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		response.ServeHTTP(w, req)
		return w.Code, w.Body.String(), nil
	}
	body := `{"folder":"/test/image","makeDefault":true}`
	const key = "platform-import-test"
	code, first, err := submit(key, body)
	if code != 202 || err != nil || !strings.Contains(first, `"status":"pending"`) {
		t.Fatalf("platform import admission = %d %s, %v; want pending", code, first, err)
	}
	if code, replay, err := submit(key, body); code != 202 || replay != first || err != nil {
		t.Fatalf("platform import replay = %d %s, %v; want identical operation", code, replay, err)
	}
	if _, _, err := submit(key, `{"folder":"/test/other","makeDefault":true}`); err == nil {
		t.Fatal("platform import accepted a different folder for the same key")
	}
	if len(ids) != 0 {
		t.Fatalf("admission ran %d external effects", len(ids))
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('admin','admin@example.test','administrator','active','local','setup','now','now',1)`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, "SELECT operation_id FROM platform_imports").Scan(&id); err != nil {
		t.Fatal(err)
	}
	restart()
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	disconnect = cancel
	lease := proc.worker().LeaseDuration
	if err := proc.worker().RunOnce(request, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted import = %v, want context canceled", err)
	}
	disconnect = nil
	restart()
	if err := proc.worker().RunOnce(ctx, id); !errors.Is(err, operation.ErrConflict) {
		t.Fatalf("import before lease expires = %v, want conflict", err)
	}
	now = now.Add(lease + time.Second)
	if err := proc.worker().RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	restart()
	if op, err := journal.Get(ctx, id); err != nil || op.Status != "succeeded" || len(ids) != 2 || ids[0] != id || ids[1] != id {
		t.Errorf("import worker = %q (%v), attempted IDs %v, want committed success and two attempts scoped to %s", op.Status, err, ids, id)
	}
	code, raw, err := submit(key, body)
	var replayed operation.Operation
	if code != 202 || err != nil || json.Unmarshal([]byte(raw), &replayed) != nil || replayed.ID != id || replayed.Status != "succeeded" {
		t.Fatalf("completed import replay = %d, %s/%s (%v), want existing completed operation %s", code, replayed.ID, replayed.Status, err, id)
	}
	const revokedKey = "platform-import-revoked"
	if code, _, err := submit(revokedKey, body); code != 202 || err != nil {
		t.Fatalf("admit import before revocation = %d, %v; want 202", code, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET membership_state='disabled' WHERE id='admin'"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT id FROM operations WHERE request_key=$1", revokedKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	restart()
	if err := proc.worker().RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	if op, err := journal.Get(ctx, id); err != nil || op.Status != "failed" || len(ids) != 2 {
		t.Errorf("import after revocation = %q, %v, %d total effects; want failure without a new effect", op.Status, err, len(ids))
	}
}

// TestEC2PlatformImportRejectsLocalFolder keeps cloud imports confined to image manifests.
func TestEC2PlatformImportRejectsLocalFolder(t *testing.T) {
	proc := NewPlatformImport(nil, nil, "", "", time.Now, nil, "ec2")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/platform-artifacts", strings.NewReader(`{"folder":"/controller/image"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cloud-import-test")
	_, err := proc.admit(t.Context(), nil, req, auth.Principal{}, web.AuthorizationTarget{}, auth.Decision{})
	var problem web.Problem
	if !errors.As(err, &problem) || problem.Code != "infrastructure.invalid_import_manifest" {
		t.Fatalf("cloud import error = %v; want an invalid manifest", err)
	}
}
