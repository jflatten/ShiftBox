package imagecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// SettingsAction is the operation journal's cache activation authority.
	SettingsAction = "infrastructure.image_cache"
	// VolumeRoot is the optional controller bind for a dedicated cache volume.
	VolumeRoot    = "/var/lib/shiftbox-registry-cache"
	storageMarker = ".shiftbox-image-cache"
)

// Settings describes administrator configuration independent of runtime state.
type Settings struct {
	Enabled           bool    `json:"enabled"`
	StoragePath       string  `json:"storagePath"`
	MaxBytes          *string `json:"maxBytes"`
	VolumeHighPercent int     `json:"volumeHighPercent"`
	VolumeLowPercent  int     `json:"volumeLowPercent"`
}

// Budget parses the exact decimal-string byte limit.
func (s Settings) Budget() int64 {
	if s.MaxBytes == nil {
		return 0
	}
	value, _ := strconv.ParseInt(*s.MaxBytes, 10, 64)
	return value
}

// Validate checks the settings without filesystem I/O.
func (s Settings) Validate() error {
	if !filepath.IsAbs(s.StoragePath) || filepath.Clean(s.StoragePath) != s.StoragePath ||
		s.StoragePath == string(filepath.Separator) || strings.ContainsAny(s.StoragePath, "\x00\r\n") {
		return errors.New("Enter a canonical absolute cache storage path")
	}
	if s.MaxBytes == nil && s.Enabled ||
		s.MaxBytes != nil && (s.Budget() <= 0 || strconv.FormatInt(s.Budget(), 10) != *s.MaxBytes) {
		return errors.New("Enter a positive whole byte limit within the signed 64-bit range")
	}
	if s.VolumeLowPercent <= 0 || s.VolumeLowPercent >= s.VolumeHighPercent || s.VolumeHighPercent >= 100 {
		return errors.New("Enter watermarks with 0% < low < high < 100%")
	}
	return nil
}

func within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// StoragePathsOverlap detects nesting between a current and proposed cache root.
func StoragePathsOverlap(a, b string) bool { return within(a, b) || within(b, a) }

// CheckStoragePath rejects paths that could share workspace, backup or key
// storage. An external path must be empty or previously owned by this cache.
func CheckStoragePath(installationDir, volumeID string, settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	path := settings.StoragePath
	installationCache := filepath.Join(installationDir, "cache")
	switch {
	case within(path, installationDir),
		within(installationDir, path) && (path == installationCache || !within(installationCache, path)):
		return errors.New("Choose a cache directory outside protected installation storage")
	case within(VolumeRoot, path):
		if path == VolumeRoot || volumeID == "" {
			return errors.New("The dedicated registry cache volume is unavailable")
		}
		marker := filepath.Join(VolumeRoot, ".shiftbox-registry-cache")
		info, err := os.Lstat(marker)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || info.Size() > 128 {
			return errors.New("The dedicated registry cache volume marker is unavailable")
		}
		markerStat, ok := info.Sys().(*syscall.Stat_t)
		var parentStat syscall.Stat_t
		if !ok || markerStat.Uid != 0 || syscall.Stat(filepath.Dir(VolumeRoot), &parentStat) != nil || markerStat.Dev == parentStat.Dev {
			return errors.New("The registry cache marker must be root-owned on the dedicated mounted volume")
		}
		data, err := os.ReadFile(marker)
		if err != nil || strings.TrimSpace(string(data)) != volumeID {
			return errors.New("The mounted registry cache volume does not match the expected volume")
		}
	}

	// Refuse symlinks at every level. The cache cannot claim another existing
	// directory just because it happens to contain familiar filenames.
	for candidate := path; candidate != string(filepath.Separator); candidate = filepath.Dir(candidate) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return errors.New("The cache path must use accessible directories without symbolic links")
		}
	}
	root, err := os.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		if !within(installationCache, path) && !within(VolumeRoot, path) {
			return errors.New("Create the external cache directory before activating it")
		}
		return nil
	}
	if err != nil {
		return errors.New("The cache directory is unavailable")
	}
	defer root.Close()
	info, err := root.Lstat(storageMarker)
	if err == nil && info.Mode().IsRegular() && info.Size() == 0 {
		return nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return errors.New("The cache directory is unavailable")
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); !errors.Is(err, io.EOF) {
		return errors.New("Choose an empty directory or a directory already managed by the registry cache")
	}
	return nil
}

// EffectiveSettings identifies the version currently in service.
type EffectiveSettings struct {
	Version int64 `json:"version"`
	Settings
}

// Activation records current staged intent or a durable activation outcome.
type Activation struct {
	Phase       string  `json:"phase"`
	Error       *string `json:"error"`
	OperationID string  `json:"operationId,omitempty"`
}

// RetiredLocation keeps a previously active path accounted for after a move.
type RetiredLocation struct {
	StoragePath string    `json:"storagePath"`
	Bytes       string    `json:"bytes"`
	RetiredAt   time.Time `json:"retiredAt"`
}

// Configuration is the durable desired and effective settings snapshot.
type Configuration struct {
	Version          int64             `json:"version"`
	Desired          Settings          `json:"desired"`
	Effective        EffectiveSettings `json:"effective"`
	Activation       Activation        `json:"activation"`
	RetiredLocations []RetiredLocation `json:"retiredLocations"`
}

// DefaultPath keeps normal installations on their existing controller volume.
func DefaultPath(installationDir, volumeID string) string {
	if volumeID != "" {
		return filepath.Join(VolumeRoot, "cache")
	}
	cache := filepath.Join(installationDir, "cache", "registry")
	if canonical, err := filepath.EvalSymlinks(installationDir); err == nil {
		cache = filepath.Join(canonical, "cache", "registry")
	}
	return cache
}

// DecodeConfiguration expands defaults only when no configuration is stored.
func DecodeConfiguration(raw string, version int64, defaultPath string) (Configuration, error) {
	if raw == "" {
		settings := Settings{StoragePath: defaultPath, VolumeHighPercent: 80, VolumeLowPercent: 70}
		return Configuration{Version: version, Desired: settings, Effective: EffectiveSettings{Version: version, Settings: settings},
			Activation: Activation{Phase: "disabled"}, RetiredLocations: []RetiredLocation{}}, nil
	}
	var config Configuration
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return Configuration{}, fmt.Errorf("decode registry cache configuration: %w", err)
	}
	config.Version = version
	return config, nil
}

// Bytes returns an exact JSON-safe representation of a byte quantity.
func Bytes(value int64) *string {
	result := strconv.FormatInt(value, 10)
	return &result
}

// RuntimeStatus captures live cache measurements independently of SQL state.
type RuntimeStatus struct {
	CachedImages  []CachedImage  `json:"cachedImages"`
	Health        Health         `json:"health"`
	Usage         *Usage         `json:"usage"`
	Effectiveness *Effectiveness `json:"effectiveness"`
}

// Health is a timestamped cache capability and availability snapshot.
type Health struct {
	State         string     `json:"state"`
	ObservedAt    *time.Time `json:"observedAt"`
	Detail        *string    `json:"detail"`
	GuestEndpoint *string    `json:"guestEndpoint"`
	Capabilities  []string   `json:"capabilities"`
}

// Usage includes retained payloads and physical volume headroom.
type Usage struct {
	RetainedBytes        *string    `json:"retainedBytes"`
	TemporaryBytes       *string    `json:"temporaryBytes"`
	ReservedBytes        *string    `json:"reservedBytes"`
	RetiredBytes         *string    `json:"retiredBytes"`
	BudgetBytes          *string    `json:"budgetBytes"`
	VolumeTotalBytes     *string    `json:"volumeTotalBytes"`
	VolumeUsedBytes      *string    `json:"volumeUsedBytes"`
	VolumeAvailableBytes *string    `json:"volumeAvailableBytes"`
	ActiveTransfers      int        `json:"activeTransfers"`
	LastEvictionAt       *time.Time `json:"lastEvictionAt"`
	LastEvictedBytes     *string    `json:"lastEvictedBytes"`
	PressureReason       *string    `json:"pressureReason"`
	ObservedAt           time.Time  `json:"observedAt"`
}

// Effectiveness reports the provenance and window of measured payload reuse.
type Effectiveness struct {
	WindowStartedAt        time.Time `json:"windowStartedAt"`
	WindowEndedAt          time.Time `json:"windowEndedAt"`
	CachedBytes            *string   `json:"cachedBytes"`
	UpstreamBytes          *string   `json:"upstreamBytes"`
	MetadataBytes          *string   `json:"metadataBytes"`
	StreamedBytes          *string   `json:"streamedBytes"`
	DirectBytes            *string   `json:"directBytes"`
	GuestLocalBytes        *string   `json:"guestLocalBytes"`
	ControllerReusePercent *float64  `json:"controllerReusePercent"`
}

// OpenSettings initializes only the directory admitted for the effective configuration.
func OpenSettings(ctx context.Context, installationDir, volumeID string, settings Settings, retired int64) (*Cache, error) {
	if !settings.Enabled {
		return New("", 0)
	}
	if err := CheckStoragePath(installationDir, volumeID, settings); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(settings.StoragePath, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(settings.StoragePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	marker, err := root.OpenFile(storageMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		err = marker.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	cache, err := New(settings.StoragePath, settings.Budget())
	if err != nil {
		return nil, err
	}
	if err := cache.FitBudget(ctx, settings.Budget(), retired); err != nil {
		cache.Close()
		return nil, err
	}
	cache.SetLimits(settings, retired)
	return cache, nil
}

// FitBudget evicts unused managed content before a smaller budget can take effect.
// The current limits remain in effect until the settings transaction commits.
func (c *Cache) FitBudget(ctx context.Context, budget, retired int64) error {
	if retired > budget {
		return errors.New("Retired cache content exceeds the requested budget")
	}
	store := c.store
	store.mu.Lock()
	defer store.mu.Unlock()
	for store.used > budget-retired-store.pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !store.evictOldest("") {
			return errors.New("Cannot reach the requested budget with cache content still in use")
		}
	}
	return nil
}

// SetLimits publishes the committed limits for future reservations.
func (c *Cache) SetLimits(settings Settings, retired int64) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.budget = settings.Budget()
	c.store.high = int64(settings.VolumeHighPercent)
	c.store.low = int64(settings.VolumeLowPercent)
	c.store.retired = retired
}

// RetiredBytes measures remaining managed files without reopening them for writes.
func RetiredBytes(path string) (int64, error) {
	root, err := os.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	info, err := root.Lstat(storageMarker)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return 0, errors.New("Retired storage could not be verified")
	}
	dir, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	var size int64
	for {
		items, err := dir.ReadDir(128)
		for _, item := range items {
			if !item.Type().IsRegular() || !managedContent(item.Name()) {
				continue
			}
			info, err := item.Info()
			if err != nil {
				return 0, err
			}
			size += info.Size()
		}
		if errors.Is(err, io.EOF) {
			return size, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// Status reports in-process accounting with unknown physical headroom explicit.
func (c *Cache) Status(now time.Time) RuntimeStatus {
	store := c.store
	store.mu.Lock()
	defer store.mu.Unlock()
	pulls, start := c.Effectiveness()
	status := RuntimeStatus{Health: Health{State: "disabled", ObservedAt: &now, Capabilities: []string{"publicPullThrough"}},
		CachedImages: []CachedImage{},
		Effectiveness: &Effectiveness{WindowStartedAt: start, WindowEndedAt: now,
			CachedBytes: Bytes(pulls.CachedBytes), UpstreamBytes: Bytes(pulls.UpstreamBytes), MetadataBytes: Bytes(pulls.MetadataBytes),
			StreamedBytes: Bytes(pulls.StreamedBytes), ControllerReusePercent: pulls.ReusePercent()}}
	if store.root == nil {
		return status
	}
	status.Health.State = "ready"
	total, available := store.volume()
	usage := &Usage{RetainedBytes: Bytes(store.used + store.retired), TemporaryBytes: Bytes(store.temporary),
		ReservedBytes: Bytes(store.pending - store.temporary), RetiredBytes: Bytes(store.retired), BudgetBytes: Bytes(store.budget),
		ActiveTransfers: len(c.slots), ObservedAt: now, LastEvictedBytes: Bytes(store.evicted)}
	if total > 0 && available >= 0 && available <= total {
		usage.VolumeTotalBytes = Bytes(total)
		usage.VolumeUsedBytes = Bytes(total - available)
		usage.VolumeAvailableBytes = Bytes(available)
	} else {
		reason := "Volume capacity could not be measured. New image content will stream."
		usage.PressureReason = &reason
		status.Health.State = "degraded"
	}
	if usage.PressureReason == nil && (!store.volumeRoom(0, store.high) || store.used+store.retired+store.pending >= store.budget) {
		reason := "Cache budget or volume watermark reached. New content streams when space cannot be reclaimed."
		status.Health.State = "pressure"
		usage.PressureReason = &reason
	}
	if !store.lastEviction.IsZero() {
		last := store.lastEviction
		usage.LastEvictionAt = &last
	}
	status.Health.Detail = usage.PressureReason
	status.Usage = usage
	return status
}

// UpdateRetiredBytes reconciles space reclaimed by the operator.
func (c *Cache) UpdateRetiredBytes(count int64) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.retired = count
}
