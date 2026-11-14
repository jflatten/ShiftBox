package backup

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

// captureCheckpoint holds the write fence only during checkpoint acquisition.
// The caller can assemble and encrypt the staged snapshots after it returns.
func captureCheckpoint(ctx context.Context, pause func(context.Context) (func(), error), capture func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resume, err := pause(ctx)
	if err != nil {
		return err
	}
	if resume == nil {
		return errors.New("backup checkpoint did not acquire a write fence")
	}
	defer resume()
	return capture(ctx)
}

// SQLiteCheckpoint captures a consistent database, including committed WAL
// changes, into an exclusive file inside the caller's private staging directory.
func SQLiteCheckpoint(ctx context.Context, db *sql.DB, path string) error {
	if db == nil || !filepath.IsAbs(path) {
		return errors.New("invalid SQLite checkpoint destination")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot reserve SQLite checkpoint destination")
	}
	if err := file.Close(); err != nil {
		return errors.Join(errors.New("cannot close SQLite checkpoint destination"), os.Remove(path))
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO $1", path); err != nil {
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), os.Remove(path))
		}
		return errors.Join(errors.New("cannot capture SQLite checkpoint"), os.Remove(path))
	}
	return nil
}

// ProtectedInputs names the retained controller files owned by the archive.
type ProtectedInputs struct {
	Configuration   string
	InstallationKey string
	GuestCA         string
}

// StageSQLiteCheckpoint captures the application and Dex stores and their
// protected configuration while the caller holds the controller write fence.
// The caller owns cleanup of the private staging directory on failure.
func StageSQLiteCheckpoint(ctx context.Context, dir string, application *sql.DB, dexPath string, inputs ProtectedInputs) (map[string]string, error) {
	if !privateCheckpointDirectory(dir) {
		return nil, errors.New("backup checkpoint requires a private staging directory")
	}
	info, err := os.Lstat(dexPath)
	if err != nil || !filepath.IsAbs(dexPath) || !info.Mode().IsRegular() {
		return nil, errors.New("Dex SQLite store is unavailable")
	}
	uri := url.URL{Scheme: "file", Path: dexPath, RawQuery: "mode=ro"}
	dex, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, errors.New("cannot open Dex SQLite store")
	}
	defer dex.Close()
	// The managed Dex always enables the password store. An empty or
	// unrelated SQLite file can pass VACUUM INTO without this check.
	var migrations int64
	if err := dex.QueryRowContext(ctx, "SELECT COALESCE(MAX(num), 0) FROM migrations").Scan(&migrations); err != nil || migrations < 1 {
		return nil, errors.New("Dex SQLite store is not initialized")
	}
	if _, err := dex.ExecContext(ctx, "SELECT email,user_id,hash FROM password WHERE 1=0"); err != nil {
		return nil, errors.New("Dex SQLite password store is unavailable")
	}

	sources := make(map[string]string, 5)
	for _, store := range []struct {
		name string
		db   *sql.DB
	}{
		{"application.db", application},
		{"dex.db", dex},
	} {
		path := filepath.Join(dir, store.name)
		if err := SQLiteCheckpoint(ctx, store.db, path); err != nil {
			return nil, err
		}
		sources[store.name] = path
	}
	if err := stageProtectedInputs(ctx, dir, inputs, sources); err != nil {
		return nil, err
	}
	return sources, nil
}

func stageProtectedInputs(ctx context.Context, dir string, inputs ProtectedInputs, sources map[string]string) error {
	for _, input := range []struct {
		name string
		path string
	}{
		{"configuration.json", inputs.Configuration},
		{"installation.key", inputs.InstallationKey},
		{"guest-ca.json", inputs.GuestCA},
	} {
		path := filepath.Join(dir, input.name)
		if err := copyProtectedInput(ctx, input.path, path); err != nil {
			return err
		}
		sources[input.name] = path
	}
	return nil
}

func copyProtectedInput(ctx context.Context, source, destination string) error {
	input, err := openCheckpoint(source)
	if err != nil {
		return errors.New("protected backup input is unavailable")
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return errors.New("backup inputs require private files")
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot stage protected backup input")
	}
	size, err := io.Copy(output, contextReader{ctx, input})
	if err := errors.Join(err, output.Close()); err != nil || size == 0 {
		return errors.New("cannot capture protected backup input")
	}
	return nil
}

func privateCheckpointDirectory(dir string) bool {
	info, err := os.Lstat(dir)
	return err == nil && filepath.IsAbs(dir) && info.IsDir() && info.Mode().Perm()&0077 == 0
}
