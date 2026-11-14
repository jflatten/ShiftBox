package backup

import (
	"context"
	"database/sql"
	"errors"
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
