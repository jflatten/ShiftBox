package backup

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jflatten/ShiftBox/internal/database"
)

// TestRestorePostgresSchemas covers preflight rejection, failure or process
// loss between schema loads, and retry against a fresh empty replacement.
func TestRestorePostgresSchemas(t *testing.T) {
	sourceApp, sourceDex := os.Getenv("SHIFTBOX_TEST_BACKUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_BACKUP_DEX_URI")
	appURI, dexURI := os.Getenv("SHIFTBOX_TEST_RESTORE_APP_URI"), os.Getenv("SHIFTBOX_TEST_RESTORE_DEX_URI")
	caPath := os.Getenv("SHIFTBOX_TEST_RESTORE_CA")
	if sourceApp == "" || sourceDex == "" || appURI == "" || dexURI == "" || caPath == "" {
		t.Skip("requires isolated source and replacement PostgreSQL databases")
	}
	openFixtureStore(t, sourceApp, "shiftbox")
	openFixtureStore(t, sourceDex, "dex")
	ctx := t.Context()
	stage := checkpointStage(t)
	_, err := StagePostgresCheckpoint(ctx, stage, sourceApp, sourceDex, checkpointInputs(t, t.TempDir()),
		func(_ context.Context) (func(), error) { return func() {}, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := restorePostgresTestTarget(t, appURI, dexURI, caPath)
	// A damaged second archive must be rejected before loading the first.
	path := filepath.Join(stage, "application.dump")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid dump"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); err == nil {
		t.Fatal("accepted damaged native archive")
	}
	if err := ValidatePostgresReplacement(ctx, target); err != nil {
		t.Fatal("damaged dump changed the empty replacement target")
	}
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("second load fails", func(t *testing.T) {
		realRestore, err := exec.LookPath("pg_restore")
		if err != nil {
			t.Fatal("locate the fixture PostgreSQL restore utility")
		}
		bin := t.TempDir()
		const script = `#!/bin/sh
for arg do
  case "$arg" in --dbname=*) loading=yes;; --schema=shiftbox) app=yes;; esac
done
if [ "$loading" = yes ] && [ "$app" = yes ]; then exit 1; fi
exec "$SHIFTBOX_TEST_REAL_PG_RESTORE" "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SHIFTBOX_TEST_REAL_PG_RESTORE", realRestore)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		err = RestorePostgresSchemas(ctx, stage, target, caPath)
		if err == nil || !strings.Contains(err.Error(), "empty replacement") {
			t.Error("second load failure must explain how to prepare an empty replacement for retry")
		}
		if err := RestorePostgresSchemas(ctx, stage, target, caPath); !errors.Is(err, ErrRestoreTarget) {
			t.Fatal("retry accepted a partially loaded replacement")
		}
		dex := openRestorePostgresFixture(t, dexURI, caPath)
		app := openRestorePostgresFixture(t, appURI, caPath)
		var kept string
		if err := dex.QueryRowContext(ctx, "SELECT note FROM dex.backup_probe").Scan(&kept); err != nil || kept != "before_cutoff" {
			t.Fatal("failed retry did not preserve the committed Dex data")
		}
		var empty bool
		if err := app.QueryRowContext(ctx, "SELECT to_regclass('shiftbox.backup_probe') IS NULL").Scan(&empty); err != nil || !empty {
			t.Fatal("failed application load left application data")
		}
		// This fixture owns only one restored object. Reprovisioning the
		// production target is an explicit operator action outside ShiftBox.
		if _, err := dex.ExecContext(ctx, "DROP TABLE dex.backup_probe"); err != nil {
			t.Fatal("could not reset the disposable Dex fixture")
		}
	})

	t.Run("restart after Dex commit", func(t *testing.T) {
		realRestore, err := exec.LookPath("pg_restore")
		if err != nil {
			t.Fatal("locate the fixture PostgreSQL restore utility")
		}
		bin := t.TempDir()
		// The Dex command has already returned successfully when the next
		// invocation reaches the application schema's load, with no app writes.
		const script = `#!/bin/sh
for arg do
  case "$arg" in --dbname=*) loading=yes;; --schema=shiftbox) app=yes;; esac
done
if [ "$loading" = yes ] && [ "$app" = yes ] && [ "$PPID" = "$SHIFTBOX_TEST_INTERRUPTED_RESTORE_PID" ]; then
  kill -KILL "$PPID"
  exit 1
fi
exec "$SHIFTBOX_TEST_REAL_PG_RESTORE" "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "pg_restore"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SHIFTBOX_TEST_REAL_PG_RESTORE", realRestore)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		run := func(stage string) error {
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPostgresRestoreProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "SHIFTBOX_TEST_RESTORE_STAGE="+stage)
			return cmd.Run()
		}
		// Run from an expendable copy because an abrupt exit skips removal
		// of the native restore's temporary password and catalog files.
		interrupted := checkpointStage(t)
		for _, name := range []string{"application.dump", "dex.dump"} {
			data, err := os.ReadFile(filepath.Join(stage, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(interrupted, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		err = run(interrupted)
		killed, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("interrupted restore process = %v, want SIGKILL between loads", err)
		}
		status, ok := killed.Sys().(syscall.WaitStatus)
		if !ok || status.Signal() != syscall.SIGKILL {
			t.Fatalf("interrupted restore process = %v, want SIGKILL between loads", err)
		}
		// A new process must refuse the occupied target during preflight,
		// before reaching the injected application-load interruption again.
		err = run(stage)
		refused, ok := errors.AsType[*exec.ExitError](err)
		if !ok || refused.ExitCode() != 17 {
			t.Fatalf("retry in a fresh process = %v, want occupied-target rejection", err)
		}
		dex := openRestorePostgresFixture(t, dexURI, caPath)
		app := openRestorePostgresFixture(t, appURI, caPath)
		var note string
		if err := dex.QueryRowContext(ctx, "SELECT note FROM dex.backup_probe").Scan(&note); err != nil || note != "before_cutoff" {
			t.Fatal("restart did not preserve the committed Dex rows")
		}
		var empty bool
		if err := app.QueryRowContext(ctx, "SELECT to_regclass('shiftbox.backup_probe') IS NULL").Scan(&empty); err != nil || !empty {
			t.Fatal("restart loaded application data into the partial target")
		}
		if _, err := dex.ExecContext(ctx, "DROP TABLE dex.backup_probe"); err != nil {
			t.Fatal("could not reset the disposable Dex fixture")
		}
	})

	if err := ValidatePostgresReplacement(ctx, target); err != nil {
		t.Fatal("reprovisioned replacement did not pass validation")
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); err != nil {
		t.Fatal(err)
	}
	if err := RestorePostgresSchemas(ctx, stage, target, caPath); !errors.Is(err, ErrRestoreTarget) {
		t.Fatalf("second load into occupied target = %v; want rejection", err)
	}
	for _, store := range []struct{ uri, schema string }{{appURI, "shiftbox"}, {dexURI, "dex"}} {
		db := openRestorePostgresFixture(t, store.uri, caPath)
		var row string
		if err := db.QueryRowContext(ctx, "SELECT note FROM "+store.schema+".backup_probe").Scan(&row); err != nil || row != "before_cutoff" {
			t.Fatalf("restored %s data = %q, %v; want checkpoint data", store.schema, row, err)
		}
	}
}

// TestPostgresRestoreProcess is the fresh process for the restart case. The
// dedicated exit code distinguishes an occupied target from a tool failure.
func TestPostgresRestoreProcess(t *testing.T) {
	stage := os.Getenv("SHIFTBOX_TEST_RESTORE_STAGE")
	if stage == "" {
		t.Skip("runs as a child of TestRestorePostgresSchemas")
	}
	t.Setenv("SHIFTBOX_TEST_INTERRUPTED_RESTORE_PID", strconv.Itoa(os.Getpid()))
	caPath := os.Getenv("SHIFTBOX_TEST_RESTORE_CA")
	target := restorePostgresTestTarget(t, os.Getenv("SHIFTBOX_TEST_RESTORE_APP_URI"), os.Getenv("SHIFTBOX_TEST_RESTORE_DEX_URI"), caPath)
	err := RestorePostgresSchemas(t.Context(), stage, target, caPath)
	if errors.Is(err, ErrRestoreTarget) {
		os.Exit(17)
	}
	t.Fatalf("restore process returned %v, want interruption or target rejection", err)
}

func restorePostgresTestTarget(t *testing.T, appURI, dexURI, caPath string) database.PostgresSetupTarget {
	t.Helper()
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal("cannot read fixture CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("cannot load fixture CA")
	}
	return database.PostgresSetupTarget{ApplicationURI: appURI, DexURI: dexURI, Roots: roots}
}

func openRestorePostgresFixture(t *testing.T, uri, caPath string) *sql.DB {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal("invalid fixture URI")
	}
	query := u.Query()
	query.Set("sslrootcert", caPath)
	u.RawQuery = query.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal("cannot open replacement schema")
	}
	t.Cleanup(func() { db.Close() })
	return db
}
