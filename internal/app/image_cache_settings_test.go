package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestImageCacheActivationDrainsBeforeMoving keeps a pull lease active while
// applying a new location, checks old bytes and effective versions, and verifies
// that a later unsafe proposal fails without changing the effective cache.
func TestImageCacheActivationDrainsBeforeMoving(t *testing.T) {
	ctx := t.Context()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(base, "shiftbox.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	oldPath := imagecache.DefaultPath(base, "")
	newPath := filepath.Join(base, "cache", "registry-next")
	budget := "100000"
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	op, err := journal.Create(ctx, operation.Request{OriginatingAction: imagecache.SettingsAction, Key: "registry-cache-test", Actor: "admin",
		Type: "setup", Generation: 2, Steps: []operation.Step{{Key: "validate", Label: "Validate"}, {Key: "drain", Label: "Drain"}, {Key: "activate", Label: "Activate"}}})
	if err != nil {
		t.Fatal(err)
	}
	settings := imagecache.Settings{Enabled: true, StoragePath: oldPath, MaxBytes: &budget, VolumeHighPercent: 80, VolumeLowPercent: 70}
	config := imagecache.Configuration{Version: 2, Desired: settings, Effective: imagecache.EffectiveSettings{Version: 1, Settings: settings},
		Activation: imagecache.Activation{Phase: "staged", OperationID: op.ID}}
	config.Desired.StoragePath = newPath
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version,image_cache_settings_json,image_cache_version)
		VALUES('99999999-0000-4000-8000-000000000001','active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test',
		'{service}--{workspace}.preview.test','fixture','fixture','now','now',1,$1,2)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	cache, err := newImageCacheRuntime(ctx, db, base, "", "https://guest.test:8444", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var closed bool
	defer func() {
		if !closed {
			_ = cache.Close()
		}
	}()
	oldBlob := filepath.Join(oldPath, "blob-sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(oldBlob, []byte("existing cache bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	worker := &operation.Worker{Journal: journal, Name: "cache-test", LeaseDuration: time.Minute, Executor: cache, Actions: []string{imagecache.SettingsAction}}
	if err := worker.RunOnce(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	cache.mu.RLock()
	err = worker.RunOnce(ctx, op.ID)
	cache.mu.RUnlock()
	if err == nil || !cache.draining.Load() {
		t.Fatalf("active lease should delay activation, got %v", err)
	}
	during, err := cache.configuration(ctx)
	if err != nil || during.Effective.Version != 1 || during.Activation.Phase != "draining" {
		t.Fatalf("during activation = %+v, %v", during, err)
	}
	for range 2 {
		if err := worker.RunOnce(ctx, op.ID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := cache.configuration(ctx)
	if err != nil || after.Effective.Version != 2 || after.Effective.StoragePath != newPath ||
		len(after.RetiredLocations) != 1 || after.RetiredLocations[0].Bytes != "20" || cache.draining.Load() {
		t.Fatalf("after activation = %+v, %v", after, err)
	}
	if value, err := os.ReadFile(oldBlob); err != nil || string(value) != "existing cache bytes" {
		t.Errorf("retired bytes = %q, %v", value, err)
	}
	if entries, err := os.ReadDir(newPath); err != nil || len(entries) != 1 {
		t.Errorf("new cache should contain only its ownership marker, got %v, %v", entries, err)
	}
	if err := os.Remove(oldBlob); err != nil {
		t.Fatal(err)
	}
	if err := cache.refreshRetiredLocations(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = cache.configuration(ctx)
	if err != nil || after.RetiredLocations[0].Bytes != "0" {
		t.Errorf("retired usage after reclamation = %+v, %v", after.RetiredLocations, err)
	}
	unsafe, err := journal.Create(ctx, operation.Request{OriginatingAction: imagecache.SettingsAction, Key: "registry-unsafe-test", Actor: "admin",
		Type: "setup", Generation: 3, Steps: []operation.Step{{Key: "validate", Label: "Validate"}}})
	if err != nil {
		t.Fatal(err)
	}
	after.Version = 3
	after.Desired.StoragePath = base
	after.Activation = imagecache.Activation{Phase: "staged", OperationID: unsafe.ID}
	raw, err = json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET image_cache_settings_json=$1,image_cache_version=3 WHERE singleton=1", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx, unsafe.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := cache.configuration(ctx)
	if err != nil || failed.Activation.Phase != "failed" || failed.Activation.Error == nil ||
		failed.Effective.Version != 2 || failed.Effective.StoragePath != newPath {
		t.Fatalf("unsafe activation = %+v, %v", failed, err)
	}
	recorded, err := journal.Get(ctx, unsafe.ID)
	if err != nil || recorded.Status != "failed" {
		t.Errorf("unsafe operation = %+v, %v", recorded, err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if err := os.RemoveAll(newPath); err != nil {
		t.Fatal(err)
	}
	restored, err := newImageCacheRuntime(ctx, db, base, "", "https://guest.test:8444", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, err := restored.status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Usage == nil || state.Usage.RetainedBytes == nil || *state.Usage.RetainedBytes != "0" || len(state.CachedImages) != 0 {
		t.Errorf("restart with empty cache = %+v, want retained settings and no payload", state)
	}
}

// TestImageCacheRuntimeDefaults checks that both caches use shared storage on a
// fresh install, an explicit public Off still permits built images, and a
// protected saved path falls back to streaming.
func TestImageCacheRuntimeDefaults(t *testing.T) {
	tests := []struct {
		name          string
		savePublicOff bool
		protectedPath bool
		wantPublic    bool
		wantBuilt     string
		wantBudget    int64
	}{
		{name: "fresh defaults", wantPublic: true, wantBuilt: "available", wantBudget: 14 << 30},
		{name: "saved public off", savePublicOff: true, wantBuilt: "available", wantBudget: 512 << 20},
		{name: "protected configured path", savePublicOff: true, protectedPath: true, wantBuilt: "unavailable", wantBudget: 512 << 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(base, "shiftbox.db")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := database.Migrate(ctx, db, "sqlite"); err != nil {
				t.Fatal(err)
			}
			raw := ""
			if test.savePublicOff {
				path := imagecache.DefaultPath(base, "")
				if test.protectedPath {
					path = base
				}
				settings := imagecache.Settings{StoragePath: path, MaxBytes: imagecache.Bytes(test.wantBudget), VolumeHighPercent: 80, VolumeLowPercent: 70}
				config := imagecache.Configuration{Version: 1, Desired: settings,
					Effective: imagecache.EffectiveSettings{Version: 1, Settings: settings}, Activation: imagecache.Activation{Phase: "disabled"}}
				value, err := json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
				raw = string(value)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,
				management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version,image_cache_settings_json)
				VALUES('99999999-0000-4000-8000-000000000009','active','ShiftBox','sqlite','https://management.test',
				'{workspace}.ide.test','{service}--{workspace}.preview.test','fixture','fixture','now','now',1,$1)`, raw); err != nil {
				t.Fatal(err)
			}
			cache, err := newImageCacheRuntime(ctx, db, base, "", "https://guest.test:8444", time.Now)
			if err != nil {
				t.Fatalf("newImageCacheRuntime = %v, want usable controller", err)
			}
			t.Cleanup(func() {
				if err := cache.Close(); err != nil {
					t.Error(err)
				}
			})
			config, err := cache.configuration(ctx)
			if err != nil {
				t.Fatalf("configuration(%s): %v", test.name, err)
			}
			if config.Desired.Enabled != test.wantPublic || config.Effective.Enabled != test.wantPublic || cache.retainPublic != test.wantPublic {
				t.Errorf("newImageCacheRuntime(%s) public retention (desired, effective, runtime) = %t, %t, %t, want all %t",
					test.name, config.Desired.Enabled, config.Effective.Enabled, cache.retainPublic, test.wantPublic)
			}
			if budget := config.Effective.Budget(); budget != test.wantBudget {
				t.Errorf("configuration(%s) budget = %d, want %d", test.name, budget, test.wantBudget)
			}
			status, err := cache.status(ctx)
			if err != nil {
				t.Fatalf("status(%s): %v", test.name, err)
			}
			if status.BuiltImageAvailability != test.wantBuilt {
				t.Errorf("status(%s) built-image availability = %s, want %s", test.name, status.BuiltImageAvailability, test.wantBuilt)
			}
		})
	}
}
