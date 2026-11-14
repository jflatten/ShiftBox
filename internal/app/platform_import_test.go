package app

import (
	"context"
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
// lets a restarted worker read it outside the authorization transaction.
func TestPlatformImportAdmission(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "fixture.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
	effects := 0
	proc := NewPlatformImport(db, journal, "https://management.test", org, func() time.Time { return now },
		func(_ context.Context, _ string, actor, folder string, makeDefault bool) error {
			effects++
			if actor != "admin" || folder != "/test/image" || !makeDefault {
				t.Errorf("worker import = %q, %q, %t; want accepted fixture input", actor, folder, makeDefault)
			}
			return nil
		})
	handler := proc.Handler()
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
		response, err := handler.Apply(ctx, tx, req, actor, web.AuthorizationTarget{}, auth.Decision{})
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
	if effects != 0 {
		t.Fatalf("admission ran %d external effects", effects)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('admin','admin@example.test','administrator','active','local','setup','now','now',1)`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, "SELECT operation_id FROM platform_imports").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := proc.worker().RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	if op, err := journal.Get(ctx, id); err != nil || op.Status != "succeeded" || effects != 1 {
		t.Errorf("import worker = %q, %v, %d effects; want committed success", op.Status, err, effects)
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
	if err := proc.worker().RunOnce(ctx, id); err != nil {
		t.Fatal(err)
	}
	if op, err := journal.Get(ctx, id); err != nil || op.Status != "failed" || effects != 1 {
		t.Errorf("import after revocation = %q, %v, %d total effects; want failure without a new effect", op.Status, err, effects)
	}
}
