package app

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestRetainPostgresDexStorage checks idempotent staging, installation-bound
// encryption and rejection of a different PostgreSQL role or database.
func TestRetainPostgresDexStorage(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "storage.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	const installation = "ce6e4d3e-2de4-4eb9-8a78-6cf8782a3662"
	const application = "postgres://app:app-fixture@db.test/shiftbox?sslmode=verify-full&connect_timeout=5"
	const dex = "postgres://dex:identity-fixture-secret@db.test:5432/shiftbox?sslmode=verify-full"
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'configuring','ShiftBox','postgres','https://manage.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','test','test','now','now',1)`, installation); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPostgresDexStorage(ctx, db, installation, application, cipher); !errors.Is(err, errPostgresDexStorage) {
		t.Fatalf("loading without a record returned %v, want missing storage error", err)
	}
	store := func(uri string) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return retainPostgresDexStorage(ctx, tx, installation, PostgresSetupCredentials{ApplicationURI: application, DexURI: uri}, cipher)
		})
	}
	for _, uri := range []string{
		strings.Replace(dex, "dex:", "app:", 1),
		strings.Replace(dex, "/shiftbox?", "/other?", 1),
		dex + "&search_path=public",
		strings.Replace(dex, "verify-full", "require", 1),
	} {
		if err := store(uri); !errors.Is(err, errPostgresDexStorage) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("rejecting inappropriate credentials returned %v, want a redacted storage error", err)
		}
	}
	for range 2 {
		if err := store(dex); err != nil {
			t.Fatal(err)
		}
	}
	if err := store(strings.Replace(dex, "fixture", "changed", 1)); !errors.Is(err, errPostgresDexStorage) {
		t.Fatalf("credential conflict returned %v, want storage error", err)
	}
	var stored string
	if err := db.QueryRowContext(ctx, "SELECT value_json FROM protected_values").Scan(&stored); err != nil ||
		strings.Contains(stored, "identity-fixture-secret") || strings.Contains(stored, "db.test") {
		t.Fatalf("protected record unavailable or exposed credentials, error %v", err)
	}
	target, err := loadPostgresDexStorage(ctx, db, installation, application, cipher)
	if err != nil || target.User != "dex" || target.Password != "identity-fixture-secret" ||
		target.Host != "db.test" || target.Port != 5432 || target.Database != "shiftbox" || target.CAFile != "system" {
		t.Fatalf("loading retained identity credentials failed, error %v", err)
	}
	if _, err := loadPostgresDexStorage(ctx, db, installation, strings.Replace(application, "db.test", "other.test", 1), cipher); !errors.Is(err, errPostgresDexStorage) {
		t.Fatalf("loading for a different server returned %v, want storage error", err)
	}
	wrongKey, err := cryptostore.New([]byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadPostgresDexStorage(ctx, db, installation, application, wrongKey); !errors.Is(err, errPostgresDexStorage) {
		t.Fatalf("loading with wrong key returned %v, want storage error", err)
	}
	// Copy authenticated ciphertext to a different record identifier.
	const otherInstallation = "22d7fd46-04f7-431d-bb5e-e8a723d60ed2"
	if _, err := db.ExecContext(ctx, "INSERT INTO protected_values(id,value_json) VALUES($1,$2)",
		dexStorageContext(otherInstallation).RecordID, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPostgresDexStorage(ctx, db, otherInstallation, application, cipher); !errors.Is(err, errPostgresDexStorage) {
		t.Fatalf("loading transplanted ciphertext returned %v, want installation binding error", err)
	}
}

// TestReplaceRecoveredDexStorage verifies replacement credentials are sealed
// for the same installation and only while it is entering recovery.
func TestReplaceRecoveredDexStorage(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "restore.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','postgres','https://manage.test','{workspace}.ide.test','{service}--{workspace}.preview.test','fixture','fixture','now','now',1)`, id); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	input := PostgresSetupCredentials{ApplicationURI: "postgres://app:fixture@db.test/replacement?sslmode=verify-full",
		DexURI: "postgres://dex:replacement-fixture@db.test/replacement?sslmode=verify-full"}
	replace := func() error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return ReplaceRecoveredDexStorage(ctx, tx, id, input, cipher)
		})
	}
	if err := replace(); err == nil {
		t.Fatal("changed Dex credentials outside recovery")
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='recovery' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := replace(); err != nil {
		t.Fatal(err)
	}
	input.DexURI = "postgres://dex:rotated-fixture@db.test/replacement?sslmode=verify-full"
	if err := replace(); err != nil {
		t.Fatal(err)
	}
	got, err := loadPostgresDexStorage(ctx, db, id, input.ApplicationURI, cipher)
	if err != nil || got.Database != "replacement" || got.Password != "rotated-fixture" {
		t.Fatal("recovery did not load the replacement Dex credentials")
	}
}
