package app_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// TestAdministratorIntentResumesWithoutReset keeps one durable owner and credential intent across retries, transaction failure, and changed inputs.
func TestAdministratorIntentResumesWithoutReset(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) { checkAdministratorIntent(t, profile) })
	}
}

func checkAdministratorIntent(t *testing.T, profile string) {
	t.Helper()
	ctx := context.Background()
	cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "setup.db"), Schema: "shiftbox_test"}
	if profile == "postgres" {
		cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if cfg.PostgresURI == "" {
			t.Skip("PostgreSQL fixture not configured")
		}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ddl, err := migrations.Files.ReadFile(profile + "/00001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(ddl), "CREATE TABLE ", "CREATE TEMPORARY TABLE ")); err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES($1,'bootstrap','ShiftBox','sqlite','https://127.0.0.1:8443','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`, installation); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	input := app.AdministratorIntent{InstallationID: installation, UserID: "00000000-0000-4000-8000-000000000002", Email: "Owner@Example.test", CreatedAt: time.Now()}
	prepare := func(tx *sql.Tx) error { _, err := app.PrepareAdministrator(ctx, tx, input, cipher); return err }
	failure := errors.New("later setup failure")
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := prepare(tx); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rolled-back setup retained owner")
	}
	var first, second app.AdministratorRecord
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		first, err = app.PrepareAdministrator(ctx, tx, input, cipher)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input.UserID = "00000000-0000-4000-8000-000000000003"
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		second, err = app.PrepareAdministrator(ctx, tx, input, cipher)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first.UserID != second.UserID || string(first.PasswordHash) != string(second.PasswordHash) {
		t.Fatal("setup retry replaced stable identity or credential")
	}
	var role, email string
	var change bool
	if err := db.QueryRow(`SELECT role,primary_email,must_change_password FROM users`).Scan(&role, &email, &change); err != nil {
		t.Fatal(err)
	}
	if role != "administrator" || email != "owner@example.test" || !change {
		t.Fatal("initial user lacks normalized identity or forced password change")
	}
	// Delivery cannot precede confirmed credential creation or consume the copy
	// when decryption fails, and a committed claim can never be repeated.
	if password, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now()); err == nil || password != "" {
		t.Fatal("password delivered before Dex readiness")
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return app.MarkAdministratorReady(ctx, tx, installation, first.UserID, []byte("stale hash"))
	}); !errors.Is(err, app.ErrSetupConflict) {
		t.Fatalf("stale readiness = %v, want conflict", err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return app.MarkAdministratorReady(ctx, tx, installation, first.UserID, first.PasswordHash)
	}); err != nil {
		t.Fatal(err)
	}
	wrongCipher, _ := cryptostore.New([]byte(strings.Repeat("x", 32)))
	if password, err := app.ClaimTemporaryPassword(ctx, db, installation, wrongCipher, time.Now()); err == nil || password != "" {
		t.Fatal("wrong key consumed delivery")
	}
	// A failed ciphertext delete must roll back the delivery marker and return
	// no plaintext. A temporary foreign-key hold injects this in both dialects.
	if _, err := db.Exec(`CREATE TEMPORARY TABLE delivery_hold(value_id TEXT REFERENCES protected_values(id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO delivery_hold(value_id) VALUES($1)`, first.UserID); err != nil {
		t.Fatal(err)
	}
	if leaked, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now()); err == nil || leaked != "" {
		t.Fatal("failed delivery transaction returned plaintext")
	}
	if _, err := db.Exec(`DELETE FROM delivery_hold`); err != nil {
		t.Fatal(err)
	}
	password, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(first.PasswordHash, []byte(password)); err != nil {
		t.Fatal("delivered password does not match durable credential")
	}
	if repeated, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now()); err == nil || repeated != "" {
		t.Fatal("temporary password was delivered twice")
	}
	if err := db.QueryRow(`SELECT count(*) FROM protected_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("delivered password ciphertext was retained")
	}
	input.Email = "different@example.test"
	if err := database.Transaction(ctx, db, prepare); !errors.Is(err, app.ErrSetupConflict) {
		t.Errorf("different initial owner = %v, want conflict", err)
	}
	if _, err := db.Exec(`UPDATE installations SET state='active'`); err != nil {
		t.Fatal(err)
	}
	input.Email = "owner@example.test"
	if err := database.Transaction(ctx, db, prepare); !errors.Is(err, app.ErrSetupConflict) {
		t.Errorf("prepare after activation = %v, want conflict", err)
	}
}
