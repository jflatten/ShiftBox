package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestBackupRoute admits only authorized requests and queues the passphrase
// after commit without including it in the response or operation records.
func TestBackupRoute(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "app.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	org := uuid.NewString()
	_, err = db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','v1','v1','now','now',1)`, org)
	if err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	checkpointParent, err := os.MkdirTemp(dir, "checkpoints-")
	if err != nil {
		t.Fatal(err)
	}
	service := backup.NewService(db, journal, backup.ArchiveMetadata{InstallationID: org, Release: "v1", Profile: "sqlite", Schema: 19}, checkpointParent)
	role := "member"
	token := strings.Repeat("A", 43)
	const pass = "fixture secret passphrase"
	var queued BackupJob
	calls := 0
	handler := BackupRoute(db, service, "https://management.test", org, time.Now, func(request BackupJob) {
		calls++
		queued = request
		if _, err := journal.Get(ctx, request.id); err != nil {
			t.Fatal("backup was handed off before admission committed")
		}
	})
	handler.Lookup = func(context.Context, *sql.Tx, string) (auth.Principal, error) {
		return auth.Principal{
			User: auth.User{ID: "operator", Role: role, MembershipState: "active", CredentialMode: "local"},
			Session: auth.Session{UserID: "operator", Mode: "normal", TokenDigest: auth.TokenDigest(token),
				CSRFDigest: auth.TokenDigest(auth.CSRFToken(token)), ExpiresAt: time.Now().Add(time.Hour)},
			InstallationState: "active", InstallationID: org,
		}, nil
	}
	output := filepath.Join(dir, "backup.age")
	send := func(key, csrf, path string, status int) string {
		t.Helper()
		body, err := json.Marshal(map[string]string{"password": pass, "destination": path})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "https://management.test/api/v1/backups", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://management.test")
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != status || strings.Contains(out.Body.String(), pass) {
			t.Fatalf("POST backup = %d %s; want %d without passphrase", out.Code, out.Body, status)
		}
		return out.Body.String()
	}
	key, csrf := "backup-fixture-key", auth.CSRFToken(token)
	send(key, csrf, output, 403)
	role = "administrator"
	send(key, "invalid", output, 403)
	send(key, csrf, "relative.age", 400)
	first := send(key, csrf, output, 202)
	if replay := send(key, csrf, output, 202); replay != first || calls != 1 {
		t.Fatalf("backup replay queued %d attempts; want only the original", calls)
	}
	send("backup-another-key", csrf, output, 409)
	if queued.password != pass || queued.destination != output || queued.id == "" {
		t.Fatal("queued backup lost its inputs")
	}
	// A process restart loses the passphrase, so abandon any unfinished job
	// before admitting another backup.
	if err := service.InterruptIncomplete(ctx); err != nil {
		t.Fatal(err)
	}
	if op, err := journal.Get(ctx, queued.id); err != nil || op.Status != "failed" {
		t.Fatalf("interrupted backup = %s, %v; want failed", op.Status, err)
	}
	send("backup-new-after-restart", csrf, output, 202)
	events, err := journal.Events(ctx, queued.id, 0)
	payload, jsonErr := json.Marshal(events)
	if err != nil || jsonErr != nil || strings.Contains(string(payload), pass) || strings.Contains(string(payload), output) {
		t.Fatal("backup events contain protected input or could not be read")
	}
	jobs := make(chan BackupJob, 1)
	jobs <- queued
	close(jobs)
	if err := RunBackupJobs(ctx, jobs, service, func(_ context.Context, stage string) (map[string]string, error) {
		sources := make(map[string]string)
		for _, name := range []string{"application.db", "dex.db", "configuration.json", "installation.key", "guest-ca.json"} {
			path := filepath.Join(stage, name)
			if err := os.WriteFile(path, []byte("fixture "+name), 0600); err != nil {
				return nil, err
			}
			sources[name] = path
		}
		return sources, nil
	}); err != nil {
		t.Fatal(err)
	}
	if op, err := journal.Get(ctx, queued.id); err != nil || op.Status != "succeeded" {
		t.Fatalf("queued backup = %s, %v; want verified success", op.Status, err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("verified archive missing: %v", err)
	}
}
