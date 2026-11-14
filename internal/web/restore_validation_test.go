package web

import (
	"context"
	"database/sql"
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
)

// TestRestoreValidationRoute checks that protected inputs cross the
// post-commit handoff once and only safe result evidence is retained.
func TestRestoreValidationRoute(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "fixture.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	org := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','v1','v1','now','now',1)`, org); err != nil {
		t.Fatal(err)
	}
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	jobs := make(chan RestoreValidationJob, 1)
	h := RestoreValidationRoute(db, journal, "https://management.test", org, time.Now, func(job RestoreValidationJob) {
		if _, err := journal.Get(ctx, job.OperationID); err != nil {
			t.Fatal("job started before admission committed")
		}
		jobs <- job
	})
	token := strings.Repeat("A", 43)
	role := "member"
	h.Lookup = func(context.Context, *sql.Tx, string) (auth.Principal, error) {
		return auth.Principal{User: auth.User{ID: "actor", Role: role, MembershipState: "active", CredentialMode: "local"},
			Session: auth.Session{UserID: "actor", Mode: "normal", TokenDigest: auth.TokenDigest(token),
				CSRFDigest: auth.TokenDigest(auth.CSRFToken(token)), ExpiresAt: time.Now().Add(time.Hour)},
			InstallationID: org, InstallationState: "active"}, nil
	}
	const password = "fixture-private-passphrase"
	const dexSecret = "dex-private-password"
	send := func(want int) {
		t.Helper()
		body := `{"archivePath":"/private/backup.age","password":"` + password + `","replacementPostgres":{"profile":"postgres","tlsMode":"verify-full","connectionUri":"postgres://app:private@localhost/replacement?sslmode=verify-full","dexConnectionUri":"postgres://dex:` + dexSecret + `@localhost/replacement?sslmode=verify-full"}}`
		r := httptest.NewRequest("POST", "https://management.test/api/v1/restores/validate", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", "https://management.test")
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", "restore-fixture-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), password) || strings.Contains(w.Body.String(), dexSecret) {
			t.Fatalf("restore response status = %d; want %d and no credentials", w.Code, want)
		}
	}
	send(403)
	role = "administrator"
	send(202)
	job := <-jobs
	send(202)
	if len(jobs) != 0 || job.Password != password || job.Postgres == nil || !strings.Contains(job.Postgres.DexConnectionURI, dexSecret) {
		t.Fatal("validation handoff omitted protected input or repeated admission")
	}
	id := uuid.NewString()
	if err := runRestoreValidation(ctx, db, journal, job, func(context.Context, RestoreValidationJob) (string, error) { return id, nil }); err != nil {
		t.Fatal(err)
	}
	op, err := journal.Get(ctx, job.OperationID)
	if err != nil || op.Status != "succeeded" || op.Steps[0].Detail != id {
		t.Fatalf("validation state = %+v, %v; want durable completed validation", op, err)
	}
	events, err := journal.Events(ctx, job.OperationID, 0)
	for _, event := range events {
		if strings.Contains(event.Evidence, password) || strings.Contains(event.Evidence, dexSecret) {
			t.Fatal("operation evidence retained credentials")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}
