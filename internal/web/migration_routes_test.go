package web

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
	"github.com/jflatten/ShiftBox/internal/release"
)

// TestMigrationPreflightOperation checks durable admission and replays,
// authority checks, and that blocked reports reach the operation Stepper.
func TestMigrationPreflightOperation(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "fixture.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	installationID := uuid.NewString()
	_, err = db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','fixture','fixture','now','now',1)`, installationID)
	if err != nil {
		t.Fatal(err)
	}
	adminID := uuid.NewString()
	_, err = db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'admin@example.test','administrator','active','local','setup','now','now',1)`, adminID)
	if err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	calls := 0
	handler, worker := MigrationPreflightRoute(db, journal, "https://management.test", installationID, time.Now,
		func(_ context.Context, id string) (release.MigrationReport, error) {
			calls++
			return release.MigrationReport{ReleaseID: id, Blockers: []release.MigrationBlocker{{Code: "migration.backup_required", Detail: "Verify a backup."}}}, nil
		})
	const origin = "https://management.test"
	token := strings.Repeat("A", 43)
	role, csrf := "administrator", auth.CSRFToken(token)
	key := "migration-fixture-key"
	now := time.Now()
	handler.Lookup = func(context.Context, *sql.Tx, string) (auth.Principal, error) {
		return auth.Principal{
			User: auth.User{ID: adminID, Role: role, MembershipState: "active", CredentialMode: "local"},
			Session: auth.Session{UserID: adminID, Mode: "normal", TokenDigest: auth.TokenDigest(token),
				CSRFDigest: auth.TokenDigest(auth.CSRFToken(token)), AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)},
			InstallationID: installationID, InstallationState: "active",
		}, nil
	}
	send := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, origin+"/api/v1/admin/migrations/preflight", strings.NewReader(`{"releaseId":"`+id+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	submit := func(id string) (operation.Operation, error) {
		t.Helper()
		out := send(id)
		if out.Code != http.StatusAccepted {
			var problem Problem
			if err := json.Unmarshal(out.Body.Bytes(), &problem); err != nil {
				return operation.Operation{}, err
			}
			return operation.Operation{}, problem
		}
		var op operation.Operation
		err := json.Unmarshal(out.Body.Bytes(), &op)
		return op, err
	}
	role = "member"
	if out := send("v1.2.3"); out.Code != http.StatusForbidden {
		t.Fatalf("member submission = %d; want 403", out.Code)
	}
	role, csrf = "administrator", "wrong"
	if out := send("v1.2.3"); out.Code != http.StatusForbidden {
		t.Fatalf("submission without valid CSRF = %d; want 403", out.Code)
	}
	csrf = auth.CSRFToken(token)
	if _, err := submit("../outside"); err == nil {
		t.Fatal("accepted path as release ID")
	}
	first, err := submit("v1.2.3")
	if err != nil || first.Status != "pending" || calls != 0 {
		t.Fatalf("admission = %s, %v, %d evaluations; want durable pending operation", first.Status, err, calls)
	}
	if op, err := submit("v1.2.3"); op.ID != first.ID || err != nil {
		t.Fatalf("replay = %q, %v; want original operation", op.ID, err)
	}
	var problem Problem
	if _, err := submit("v1.2.4"); !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("changed release replay = %v; want conflict", err)
	}
	if err := worker.RunOnce(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	op, err := journal.Get(ctx, first.ID)
	if err != nil || op.Status != "failed" || !strings.Contains(op.Steps[0].Detail, "migration.backup_required") || calls != 1 {
		t.Fatalf("blocked preflight = %+v, %v, %d evaluations; want reported blocker", op, err, calls)
	}
	key = "migration-revoked-key"
	second, err := submit("v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE users SET membership_state='disabled' WHERE id=$1", adminID); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	op, err = journal.Get(ctx, second.ID)
	if err != nil || op.Status != "failed" || !strings.Contains(op.Steps[0].Detail, "authorization") || calls != 1 {
		t.Fatalf("preflight after revocation = %+v, %v, %d evaluations; want authorization failure without another evaluation", op, err, calls)
	}
}
