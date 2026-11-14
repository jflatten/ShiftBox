package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// ActivateSQLiteRestore prepares a verified SQLite stage for recovery and
// publishes it into a new protected replacement directory. The caller must
// hold the installation lock and bind the stage to the validated target.
func ActivateSQLiteRestore(ctx context.Context, stage, destination, installationID, validationID, actor string, now time.Time) (string, error) {
	if !privateCheckpointDirectory(stage) {
		return "", ErrRestoreTarget
	}
	for _, name := range []string{"dex.db", "configuration.json", "guest-ca.json", "installation.key"} {
		path := filepath.Join(stage, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && name == "installation.key" {
			// StageRestore omits the archived key if a retained key matched.
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() == 0 {
			return "", ErrRestoreTarget
		}
	}
	var review string
	err := publishSQLiteRestore(ctx, stage, destination, func(ctx context.Context, db *sql.DB) error {
		if err := database.CheckSchema(ctx, db, "sqlite"); err != nil {
			return err
		}
		journal := operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
		var err error
		review, err = EnterRecovery(ctx, db, journal, installationID, validationID, actor, now)
		return err
	})
	if err != nil {
		return "", err
	}
	return review, nil
}

// publishSQLiteRestore prepares the application database in the private
// stage, then publishes the whole directory at a new same-parent path.
func publishSQLiteRestore(ctx context.Context, stage, destination string, prepare func(context.Context, *sql.DB) error) error {
	parent := filepath.Dir(stage)
	if !privateCheckpointDirectory(stage) || !privateCheckpointDirectory(parent) ||
		filepath.Clean(destination) != destination || filepath.Dir(destination) != parent || prepare == nil {
		return ErrRestoreTarget
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return ErrRestoreTarget
	}
	path := filepath.Join(stage, "application.db")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ErrRestoreTarget
	}
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		return ErrRestoreTarget
	}
	if err := errors.Join(prepare(ctx, db), db.Close()); err != nil {
		return err
	}
	if err := syncRestoreDirectory(stage); err != nil {
		return ErrRestoreTarget
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishRestoreDirectory(stage, destination); err != nil {
		return ErrRestoreTarget
	}
	if err := syncRestoreDirectory(parent); err != nil {
		return errors.New("restore was published but its directory durability could not be confirmed")
	}
	return nil
}

func syncRestoreDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
