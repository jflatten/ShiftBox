package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/operation"
)

type imageCacheRuntime struct {
	db                         *sql.DB
	installationDir, volumeID  string
	defaultPath, guestEndpoint string
	now                        func() time.Time

	// The active cache and its storage stay open while transfers hold a
	// read lease. A separate streaming instance handles new pulls during drain.
	mu          sync.RWMutex
	draining    atomic.Bool
	active      *imagecache.Cache
	stream      *imagecache.Cache
	unavailable bool
	retaining   bool
}

func newImageCacheRuntime(ctx context.Context, db *sql.DB, installationDir, volumeID, endpoint string, now func() time.Time) (*imageCacheRuntime, error) {
	if canonical, err := filepath.EvalSymlinks(installationDir); err == nil {
		installationDir = canonical
	}
	cache := &imageCacheRuntime{db: db, installationDir: installationDir, volumeID: volumeID,
		defaultPath: imagecache.DefaultPath(installationDir, volumeID), guestEndpoint: endpoint, now: now}
	config, err := cache.configuration(ctx)
	if err != nil {
		return nil, err
	}
	cache.stream, err = imagecache.New("", 0)
	if err != nil {
		return nil, err
	}
	cache.active = cache.stream
	cache.draining.Store(config.Activation.Phase == "draining")
	if config.Effective.Enabled {
		active, err := imagecache.OpenSettings(ctx, installationDir, volumeID, config.Effective.Settings, retiredTotal(config))
		if err == nil {
			cache.active = active
			cache.retaining = true
		} else {
			cache.unavailable = true
			slog.Warn("registry retention unavailable; continuing with image streaming", "reason", err)
		}
	}
	return cache, nil
}

func (c *imageCacheRuntime) configuration(ctx context.Context) (imagecache.Configuration, error) {
	var raw string
	var version int64
	if err := c.db.QueryRowContext(ctx, "SELECT image_cache_settings_json,image_cache_version FROM installations WHERE singleton=1").Scan(&raw, &version); err != nil {
		return imagecache.Configuration{}, err
	}
	return imagecache.DecodeConfiguration(raw, version, c.defaultPath)
}

func (c *imageCacheRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.draining.Load() && c.mu.TryRLock() {
		if !c.draining.Load() {
			defer c.mu.RUnlock()
			c.active.ServeHTTP(w, r)
			return
		}
		c.mu.RUnlock()
	}
	c.stream.ServeHTTP(w, r)
}

func (c *imageCacheRuntime) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == c.stream {
		return c.stream.Close()
	}
	return errors.Join(c.active.Close(), c.stream.Close())
}

func (c *imageCacheRuntime) offer(ctx context.Context, contact guest.Contact) string {
	var policy string
	if err := c.db.QueryRowContext(ctx, `SELECT network_policy_snapshot_json FROM workspaces WHERE id=$1 AND generation=$2`,
		contact.WorkspaceID, contact.Generation).Scan(&policy); err != nil {
		return "mirror"
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return guest.ImageCacheMode(policy, c.retaining)
}

func (c *imageCacheRuntime) status() imagecache.RuntimeStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	status := c.active.Status(c.now().UTC())
	status.Health.GuestEndpoint = &c.guestEndpoint
	status.CachedImages = c.active.Inventory()
	if c.unavailable {
		reason := "The effective storage path is unavailable. Restricted workspaces can still stream public images."
		status.Health.State = "degraded"
		status.Health.Detail = &reason
	}
	return status
}

func (c *imageCacheRuntime) Observe(ctx context.Context, op operation.Operation, _ operation.Step) (string, bool, error) {
	config, err := c.configuration(ctx)
	if err != nil {
		return "", false, err
	}
	if config.Effective.Version >= op.Generation {
		return "Cache settings are active", true, nil
	}
	if config.Activation.OperationID != op.ID || config.Version != op.Generation {
		return "", false, &operation.StepFailure{Detail: "The staged cache configuration changed"}
	}
	if config.Activation.Phase == "failed" && config.Activation.Error != nil {
		return "", false, &operation.StepFailure{Detail: *config.Activation.Error}
	}
	return "", false, nil
}

func (c *imageCacheRuntime) Apply(ctx context.Context, op operation.Operation, step operation.Step) (string, error) {
	config, err := c.configuration(ctx)
	if err != nil {
		return "", err
	}
	if config.Version != op.Generation || config.Activation.OperationID != op.ID {
		return "", operation.ErrConflict
	}
	switch step.Key {
	case "validate":
		if config.Desired.Enabled {
			desiredPath := config.Desired.StoragePath
			if config.Effective.Enabled && desiredPath != config.Effective.StoragePath &&
				imagecache.StoragePathsOverlap(desiredPath, config.Effective.StoragePath) {
				return "", c.activationFailure(ctx, config, "Choose a location outside the effective cache directory")
			}
			for _, location := range config.RetiredLocations {
				if imagecache.StoragePathsOverlap(desiredPath, location.StoragePath) && desiredPath != location.StoragePath {
					return "", c.activationFailure(ctx, config, "Choose a location outside previously retired cache directories")
				}
				if desiredPath == location.StoragePath && desiredPath != config.Effective.StoragePath {
					if count, err := imagecache.RetiredBytes(location.StoragePath); err != nil || count > 0 {
						return "", c.activationFailure(ctx, config, "Reclaim the retired cache files before reusing this location")
					}
				}
			}
			if err := imagecache.CheckStoragePath(c.installationDir, c.volumeID, config.Desired); err != nil {
				return "", c.activationFailure(ctx, config, err.Error())
			}
		}
		return "Cache settings and storage path validated", nil
	case "drain":
		c.draining.Store(true)
		if config.Activation.Phase != "draining" {
			config.Activation.Phase = "draining"
			if err := c.save(ctx, config); err != nil {
				c.draining.Store(false)
				return "", err
			}
		}
		if !c.mu.TryLock() {
			return "", &operation.StepWaiting{Detail: "Waiting for active image transfers to finish"}
		}
		c.mu.Unlock()
		return "Active image transfers finished", nil
	case "activate":
		if !c.mu.TryLock() {
			return "", &operation.StepWaiting{Detail: "Waiting for active image transfers to finish"}
		}
		defer c.mu.Unlock()
		return c.activate(ctx, config)
	default:
		return "", &operation.StepFailure{Detail: "Unknown registry cache operation step"}
	}
}

func (c *imageCacheRuntime) activate(ctx context.Context, config imagecache.Configuration) (string, error) {
	var retired []imagecache.RetiredLocation
	for _, location := range config.RetiredLocations {
		if location.StoragePath != config.Desired.StoragePath || !config.Desired.Enabled {
			if bytes, err := imagecache.RetiredBytes(location.StoragePath); err == nil {
				location.Bytes = *imagecache.Bytes(bytes)
			}
			retired = append(retired, location)
		}
	}
	if config.Effective.Enabled && (!config.Desired.Enabled || config.Desired.StoragePath != config.Effective.StoragePath) {
		bytes, err := imagecache.RetiredBytes(config.Effective.StoragePath)
		if err != nil {
			return "", c.activationFailure(ctx, config, "The previous cache storage could not be measured safely")
		}
		retired = append(retired, imagecache.RetiredLocation{StoragePath: config.Effective.StoragePath, Bytes: *imagecache.Bytes(bytes), RetiredAt: c.now().UTC()})
	}
	if retired == nil {
		retired = []imagecache.RetiredLocation{}
	}
	next := config
	next.RetiredLocations = retired
	next.Effective = imagecache.EffectiveSettings{Version: next.Version, Settings: next.Desired}
	next.Activation.Phase = "disabled"
	active := c.stream
	retiredBytes := retiredTotal(next)
	reuseStorage := c.retaining && next.Desired.Enabled && config.Effective.StoragePath == next.Desired.StoragePath
	if next.Desired.Enabled {
		if reuseStorage {
			active = c.active
			if err := active.FitBudget(ctx, next.Desired.Budget(), retiredBytes); err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", c.activationFailure(ctx, config, err.Error())
			}
		} else {
			var err error
			active, err = imagecache.OpenSettings(ctx, c.installationDir, c.volumeID, next.Desired, retiredBytes)
			if err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				return "", c.activationFailure(ctx, config, "Could not open the requested registry cache storage within the settings limits")
			}
		}
		next.Activation.Phase = "ready"
	}
	if err := c.save(ctx, next); err != nil {
		if active != c.stream && active != c.active {
			_ = active.Close()
		}
		return "", err
	}
	previous := c.active
	if reuseStorage {
		active.SetLimits(next.Desired, retiredBytes)
	}
	c.active, c.retaining, c.unavailable = active, next.Effective.Enabled, false
	c.draining.Store(false)
	if previous != c.stream && previous != c.active {
		_ = previous.Close()
	}
	return "Cache settings are active", nil
}

func (c *imageCacheRuntime) activationFailure(ctx context.Context, config imagecache.Configuration, reason string) error {
	config.Activation.Phase, config.Activation.Error = "failed", &reason
	if err := database.Transaction(ctx, c.db, func(tx *sql.Tx) error {
		raw, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE installations SET image_cache_settings_json=$1
			WHERE singleton=1 AND image_cache_version=$2`, string(raw), config.Version); err != nil {
			return err
		}
		var installation string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM installations WHERE singleton=1").Scan(&installation); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: c.now(), ActorType: "controller", ActorID: "registry-cache",
			Action: "infrastructure.image_cache.activate", ResourceType: "installation", ResourceID: installation,
			Outcome: "failed", ReasonCode: "storage_unavailable"})
		return err
	}); err != nil {
		return err
	}
	c.draining.Store(false)
	return &operation.StepFailure{Detail: reason}
}

func (c *imageCacheRuntime) save(ctx context.Context, config imagecache.Configuration) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	result, err := c.db.ExecContext(ctx, `UPDATE installations SET image_cache_settings_json=$1
		WHERE singleton=1 AND image_cache_version=$2 AND state='active'`, string(raw), config.Version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return operation.ErrConflict
	}
	return err
}

func retiredTotal(config imagecache.Configuration) int64 {
	var total int64
	for _, location := range config.RetiredLocations {
		bytes, _ := strconv.ParseInt(location.Bytes, 10, 64)
		total += bytes
	}
	return total
}

func (c *imageCacheRuntime) refreshRetiredLocations(ctx context.Context) error {
	config, err := c.configuration(ctx)
	if err != nil || config.Activation.Phase == "staged" || config.Activation.Phase == "draining" {
		return err
	}
	var changed bool
	for index, location := range config.RetiredLocations {
		count, err := imagecache.RetiredBytes(location.StoragePath)
		if err != nil {
			continue // Keep the last measured size when a mount is temporarily unavailable.
		}
		measured := strconv.FormatInt(count, 10)
		if location.Bytes != measured {
			config.RetiredLocations[index].Bytes = measured
			changed = true
		}
	}
	if !changed || !c.mu.TryLock() {
		return nil
	}
	defer c.mu.Unlock()
	if err := c.save(ctx, config); err != nil {
		return err
	}
	c.active.UpdateRetiredBytes(retiredTotal(config))
	return nil
}

// ConfigureImageCache opens effective storage before starting guest listeners.
func (s *ControllerState) ConfigureImageCache(ctx context.Context, dir, volumeID, endpoint string, now func() time.Time) error {
	cache, err := newImageCacheRuntime(ctx, s.DB, dir, volumeID, endpoint, now)
	if err != nil {
		return err
	}
	s.imageCache = cache
	return nil
}

// RunImageCacheOperations reconciles durable settings changes.
func (s *ControllerState) RunImageCacheOperations(ctx context.Context, journal *operation.Journal, fence *backup.WriteFence) error {
	worker := operation.Worker{Journal: journal, Name: "image-cache", LeaseDuration: time.Minute, Executor: s.imageCache,
		Actions: []string{imagecache.SettingsAction}, EnterActivity: fence.Enter}
	return worker.Run(ctx, time.Second)
}

// RunImageCacheVerification refreshes the recorded source of running stale images.
func (s *ControllerState) RunImageCacheVerification(ctx context.Context, fence *backup.WriteFence) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := withCheckpointActivity(ctx, fence, func() error {
				return s.imageCache.refreshRetiredLocations(ctx)
			}); err != nil && !errors.Is(err, operation.ErrConflict) && ctx.Err() == nil {
				slog.Warn("could not refresh retired image cache usage")
			}
			// Reference verification has no retention side effects, so only
			// guard its authoritative observations with the checkpoint fence.
			if err := verifyWorkspaceImageFreshness(ctx, s.DB, s.imageCache.stream, fence); err != nil && ctx.Err() == nil {
				slog.Warn("could not refresh workspace image verification")
			}
		}
	}
}
