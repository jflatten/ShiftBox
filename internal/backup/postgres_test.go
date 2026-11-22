package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresCheckpointSharesSnapshot proves both native dumps see the same
// cutoff, even when unrelated and scoped data change after the fence releases.
func TestPostgresCheckpointSharesSnapshot(t *testing.T) {
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_BACKUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_BACKUP_DEX_URI")
	if appURI == "" || dexURI == "" {
		t.Skip("requires the disposable PostgreSQL backup fixture")
	}
	ctx := t.Context()
	app := openFixtureStore(t, appURI, "shiftbox")
	dex := openFixtureStore(t, dexURI, "dex")
	dir := checkpointStage(t)
	resumed := false
	var resumeErr error
	pause := func(context.Context) (func(), error) {
		return func() {
			resumed = true
			for _, store := range []struct {
				db     *sql.DB
				schema string
			}{{app, "shiftbox"}, {dex, "dex"}} {
				_, err := store.db.ExecContext(ctx, "INSERT INTO "+store.schema+".backup_probe VALUES ('after_cutoff')")
				resumeErr = errors.Join(resumeErr, err)
			}
		}, nil
	}
	sources, err := StagePostgresCheckpoint(ctx, dir, appURI, dexURI, checkpointInputs(t, t.TempDir()), pause)
	if err != nil || resumeErr != nil || !resumed {
		t.Fatalf("shared snapshot failed: %v, resumed %t (%v)", err, resumed, resumeErr)
	}
	if len(sources) != 5 {
		t.Fatalf("got %d checkpoint entries, want 5", len(sources))
	}
	for name, schema := range map[string]string{"application.dump": "shiftbox", "dex.dump": "dex"} {
		data, err := exec.CommandContext(ctx, "pg_restore", "--file=-", sources[name]).Output()
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		output := string(data)
		if !strings.Contains(output, "COPY "+schema+".backup_probe") || !strings.Contains(output, "before_cutoff") ||
			strings.Contains(output, "after_cutoff") || strings.Contains(output, "foreign_probe") {
			t.Errorf("%s did not contain only the matching schema's pre-fence data", name)
		}
		info, err := os.Stat(sources[name])
		if err != nil || info.Mode().Perm() != 0600 {
			t.Errorf("%s is not a private file: %v", name, err)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 5 {
		t.Errorf("staging retains files outside the backup allowlist: %v", err)
	}
	metadata := ArchiveMetadata{InstallationID: "fixture", Release: "development", Profile: "postgres", Schema: 18}
	path := filepath.Join(dir, "management.age")
	if _, err := CreateArchive(ctx, path, "test-only PostgreSQL backup password", metadata, sources); err != nil {
		t.Fatalf("cannot encrypt and verify the staged PostgreSQL dumps: %v", err)
	}
}

// TestPostgresCheckpointRejectsDifferentDatabase checks that a mismatched identity
// endpoint cannot acquire the write fence or start an external process.
func TestPostgresCheckpointRejectsDifferentDatabase(t *testing.T) {
	paused := false
	pause := func(context.Context) (func(), error) {
		paused = true
		return func() {}, nil
	}
	app := "postgres://app:secret@localhost/shiftbox?sslmode=verify-full"
	dex := "postgres://dex:different@localhost/other?sslmode=verify-full"
	sources, err := StagePostgresCheckpoint(t.Context(), checkpointStage(t), app, dex, ProtectedInputs{}, pause)
	if err == nil || paused || len(sources) > 0 ||
		strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "different") {
		t.Fatal("mismatched stores did not fail before fencing with a safe diagnostic")
	}
}

func openFixtureStore(t *testing.T, uri, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal("cannot open disposable fixture store")
	}
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		"CREATE TABLE IF NOT EXISTS " + schema + ".backup_probe (note text)",
		"TRUNCATE TABLE " + schema + ".backup_probe",
		"INSERT INTO " + schema + ".backup_probe VALUES ('before_cutoff')",
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("cannot seed disposable %s schema", schema)
		}
	}
	return db
}

// TestDumpPostgresSchemaKeepsPasswordPrivate uses a helper process to verify
// that the login stays in the protected password file for the export.
func TestDumpPostgresSchemaKeepsPasswordPrivate(t *testing.T) {
	dir := checkpointStage(t)
	binary := filepath.Join(dir, "dump-fixture")
	const password = `test:pass\word`
	script := `#!/bin/sh
case "$*" in *'test:pass'*|*'test%3Apass'*) exit 1;; esac
[ -z "$PGPASSWORD" ] || exit 2
[ "$(cat "$PGPASSFILE")" = '*:*:*:*:test\:pass\\word' ] || exit 4
printf 'fixture dump'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSWORD", password)
	target, err := parseDumpTarget("postgres://app:test%3Apass%5Cword@localhost/shiftbox?sslmode=verify-full", "shiftbox", "application.dump")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, target.filename)
	if err := dumpPostgresSchema(t.Context(), binary, output, "snapshot", target); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 2 {
		t.Fatalf("helper left a password file behind: %v", err)
	}
}

// TestPostgresCheckpointDrainsDexWriter checks that the identity-side write
// lock waits for an already active Dex transaction rather than skipping it.
func TestPostgresCheckpointDrainsDexWriter(t *testing.T) {
	uri := os.Getenv("SHIFTBOX_TEST_BACKUP_DEX_URI")
	if uri == "" {
		t.Skip("requires the disposable PostgreSQL backup fixture")
	}
	ctx := t.Context()
	db := openFixtureStore(t, uri, "dex")
	active, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Rollback()
	if _, err := active.ExecContext(ctx, "INSERT INTO dex.backup_probe VALUES ('transaction')"); err != nil {
		t.Fatal(err)
	}
	target, err := parseDumpTarget(uri, "dex", "dex.dump")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := connectDumpOwner(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	release, err := lockPostgresDex(short, conn)
	if err == nil {
		release()
		t.Fatal("checkpoint passed while an existing Dex writer still held its transaction")
	}
}
