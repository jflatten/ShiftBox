package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const profileAppURI = "postgres://app:fixture-application-password@db.test/shiftbox?sslmode=verify-full"
const profileDexURI = "postgres://dex:fixture-identity-password@db.test/shiftbox?sslmode=verify-full"

// TestPostgresProfileSelection verifies authentication before database probes,
// redacted errors, restart before and after commit, and the data profile gate.
func TestPostgresProfileSelection(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	clock := func() time.Time { return now }
	const origin = "https://127.0.0.1:8443"
	path := filepath.Join(t.TempDir(), "bootstrap.db")
	seed := database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	}
	open := func() *sql.DB {
		t.Helper()
		db, _, err := database.OpenBootstrap(ctx, path, seed)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db, id := open(), seed.ID
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	session, token, err := auth.NewSession("session", owner, "bootstrap_restricted", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := PrepareAdministrator(ctx, tx, AdministratorIntent{InstallationID: id, UserID: owner, Email: "owner@example.test", CreatedAt: now}, cipher); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET must_change_password=0 WHERE id=$1", owner); err != nil {
			return err
		}
		return auth.NewStore(db).SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	var probes int
	refuse := true
	var interrupt context.CancelFunc
	preflight := func(_ context.Context, target database.PostgresSetupTarget) error {
		probes++
		if target.ApplicationURI != profileAppURI || target.DexURI != profileDexURI {
			t.Error("preflight did not receive the scoped credentials")
		}
		if refuse {
			return errors.New("fixture-application-password")
		}
		if interrupt != nil {
			interrupt()
		}
		return nil
	}
	var setup SetupHTTP
	var handler http.Handler
	attach := func() {
		t.Helper()
		setup = SetupHTTP{DB: db, InstallationID: id, Origin: origin, Cipher: cipher, Now: clock,
			Journal: operation.NewJournal(db, clock, uuid.NewString), Postgres: &PostgresProfileSetup{DexURI: profileDexURI}}
		var err error
		handler, err = ProtectBootstrap(origin, setup.profileHandler(preflight))
		if err != nil {
			t.Fatal(err)
		}
	}
	attach()
	restart := func() {
		t.Helper()
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = open()
		attach()
	}
	send := func(ctx context.Context, payload, key, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, origin+"/api/v1/setup/data-profile", strings.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:61400"
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	request := func(payload, key, csrf string, want int) string {
		t.Helper()
		w := send(ctx, payload, key, csrf)
		if w.Code != want || strings.Contains(w.Body.String(), "fixture-application-password") || strings.Contains(w.Body.String(), "fixture-identity-password") {
			t.Fatalf("profile status = %d, want %d with protected credentials", w.Code, want)
		}
		return w.Body.String()
	}
	const payload = `{"profile":"postgres","connectionUri":"` + profileAppURI + `","tlsMode":"verify-full"}`
	const key = "fixture-profile-postgres"
	csrf := auth.CSRFToken(token)
	request(payload, key, "invalid", 403)
	if probes != 0 {
		t.Fatal("unauthorized request triggered a database probe")
	}
	request(payload, key, csrf, 409)
	if _, err := setup.readPendingPostgres(ctx, db); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed probe changed pending profile, error %v", err)
	}
	refuse = false
	// The first controller stops after a successful probe, before the
	// transaction can persist either the credentials or the operation.
	probeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupt = cancel
	interrupted := send(probeContext, payload, key, csrf)
	interrupt = nil
	if interrupted.Code == http.StatusAccepted || probes != 2 {
		t.Fatalf("interrupted selection status = %d after %d probes, want an uncommitted successful probe", interrupted.Code, probes)
	}
	restart()
	if _, err := setup.readPendingPostgres(ctx, db); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pending profile after interrupted probe = %v, want no row", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operations WHERE request_key=$1", key).Scan(&count); err != nil || count != 0 {
		t.Fatalf("operations after interrupted probe = %d (%v), want zero", count, err)
	}
	first := request(payload, key, csrf, 202)
	restart()
	again := request(payload, key, csrf, 202)
	if first != again || probes != 3 {
		t.Fatalf("profile replay changed response or called preflight again, probes = %d", probes)
	}
	request(strings.Replace(payload, "fixture-application-password", "other-password", 1), key, csrf, 409)
	if probes != 3 {
		t.Fatal("conflicting replay triggered a database probe")
	}
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", setup.pendingPostgresContext().RecordID).Scan(&raw); err != nil ||
		strings.Contains(raw, "fixture-application-password") || strings.Contains(raw, "fixture-identity-password") {
		t.Fatalf("selected profile was not stored as ciphertext, error %v", err)
	}
	pending, err := setup.readPendingPostgres(ctx, db)
	if err != nil || pending.Credentials.ApplicationURI != profileAppURI || pending.Credentials.DexURI != profileDexURI {
		t.Fatalf("could not load selected profile, error %v", err)
	}
	request(`{"profile":"sqlite"}`, "fixture-profile-sqlite", csrf, 202)
	if _, err := setup.readPendingPostgres(ctx, db); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("changing to SQLite left PostgreSQL selected, error %v", err)
	}
	request(payload, key, csrf, 409)
}
