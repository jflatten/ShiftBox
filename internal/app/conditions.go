package app

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

type operationalProbes struct {
	provider         func(context.Context) (provider.Capabilities, error)
	key              func() bool
	space            func() (uint64, error)
	minimumDiskBytes uint64
	startedAt        time.Time
}

type operationalCondition struct {
	key, severity, detail string
}

var (
	providerUnavailable   = operationalCondition{"provider.unavailable", "critical", "Check UTM, its supported version, and macOS automation access."}
	keyUnavailable        = operationalCondition{"encryption_key.unavailable", "critical", "Restore the original protected installation key before relying on this installation's encrypted data."}
	storageLow            = operationalCondition{"storage.low_space", "warning", "Free space on the UTM volume before creating new workspaces. Existing workspace data is retained."}
	storageUnknown        = operationalCondition{"storage.unavailable", "critical", "Check that ShiftBox can measure available space on the configured UTM volume."}
	controllerUnavailable = operationalCondition{"controller.unavailable", "critical", "The controller stopped unexpectedly. Check the controller service and its dependencies."}
)

// AvailableWorkspaceSpace reports allocatable bytes on the workspace volume.
func AvailableWorkspaceSpace(path string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, errors.New("cannot measure workspace storage")
	}
	size := uint64(stats.Bsize)
	if size == 0 || uint64(stats.Bavail) > math.MaxUint64/size {
		return 0, errors.New("invalid workspace storage measurement")
	}
	return uint64(stats.Bavail) * size, nil
}

// RunOperationalConditions periodically records current host conditions. Failed
// samples leave the last observation available and retry on the next interval.
func RunOperationalConditions(ctx context.Context, db *sql.DB, dir, storage string, minimumDiskBytes uint64,
	capabilities func(context.Context) (provider.Capabilities, error), fence *backup.WriteFence,
) error {
	keyPath := filepath.Join(dir, "installation.key")
	// Compare key bytes as well as file permissions. The controller still uses
	// the key loaded at startup until it shuts down.
	initial, err := cryptostore.LoadKey(keyPath)
	if err != nil {
		return errors.New("cannot monitor the protected installation key")
	}
	defer clear(initial)
	probes := operationalProbes{
		provider: capabilities, minimumDiskBytes: minimumDiskBytes, startedAt: time.Now(),
		space: func() (uint64, error) { return AvailableWorkspaceSpace(storage) },
		key: func() bool {
			current, err := cryptostore.LoadKey(keyPath)
			defer clear(current)
			return err == nil && subtle.ConstantTimeCompare(current, initial) == 1
		},
	}
	return runHistoryRetention(ctx, time.Minute, func(ctx context.Context) error {
		return withCheckpointActivity(ctx, fence, func() error {
			return sampleOperationalConditions(ctx, db, probes, time.Now())
		})
	}, func(error) {
		slog.WarnContext(ctx, "Could not record infrastructure health; check database availability")
	})
}

func sampleOperationalConditions(ctx context.Context, db *sql.DB, probes operationalProbes, now time.Time) error {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	caps, providerErr := probes.provider(probe)
	cancel()
	available, spaceErr := probes.space()
	keyHealthy := probes.key()
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, item := range []struct {
			condition operationalCondition
			active    bool
		}{
			{providerUnavailable, providerErr != nil || !caps.Create || !caps.Start || !caps.Stop || !caps.Delete},
			{keyUnavailable, !keyHealthy},
			{storageLow, spaceErr == nil && available < probes.minimumDiskBytes},
			{storageUnknown, spaceErr != nil},
		} {
			if err := writeOperationalCondition(ctx, tx, item.condition, item.active, now); err != nil {
				return err
			}
		}
		// Give an administrator time to see the previous failure after restart.
		if now.Sub(probes.startedAt) >= time.Minute {
			return writeOperationalCondition(ctx, tx, controllerUnavailable, false, now)
		}
		return nil
	})
}

// RecordControllerFailure retains a safe summary across an unexpected shutdown.
func RecordControllerFailure(ctx context.Context, db *sql.DB, now time.Time) error {
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return writeOperationalCondition(ctx, tx, controllerUnavailable, true, now)
	})
}

func writeOperationalCondition(ctx context.Context, tx *sql.Tx, condition operationalCondition, active bool, now time.Time) error {
	if !active {
		_, err := tx.ExecContext(ctx, "DELETE FROM operational_conditions WHERE condition_key=$1", condition.key)
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO operational_conditions(condition_key,severity,detail,observed_at)
		VALUES($1,$2,$3,$4) ON CONFLICT(condition_key) DO UPDATE SET observed_at=excluded.observed_at`,
		condition.key, condition.severity, condition.detail, now.UTC().Format(time.RFC3339Nano))
	return err
}
