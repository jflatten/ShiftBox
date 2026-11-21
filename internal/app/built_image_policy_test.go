package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestBuiltImagePolicyFencesCurrentTransfers checks that disabling the policy
// cancels active reads and writes and denies new admissions before purge runs.
func TestBuiltImagePolicyFencesCurrentTransfers(t *testing.T) {
	ctx := t.Context()
	db := seedBuiltImageAuthority(t)
	cache := openBuiltPolicyRuntime(t, db, t.TempDir())
	t.Cleanup(func() { _ = cache.Close() })
	if got := cache.builtImageAvailability(); got != "available" {
		t.Fatalf("builtImageAvailability before disable = %q, want available", got)
	}
	registry := &proxy.Registry{}
	var publicCanceled bool
	unregister, err := registry.Register(proxy.Scope{WorkspaceID: "workspace-first"}, func() error { return nil }, func() error {
		publicCanceled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)

	contact := guest.Contact{WorkspaceID: "workspace-first", OwnerID: builtAuthorityOwner,
		Repository: builtAuthorityRepository, Generation: builtAuthorityGeneration, DesiredState: "running"}
	load := func() (builtImageAuthority, error) {
		var authority builtImageAuthority
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			var err error
			authority, err = loadBuiltImageAuthority(ctx, tx, contact, "launch-first", builtAuthorityDispatch, false)
			return err
		})
		return authority, err
	}
	admit := func(grantID string, authority builtImageAuthority) (context.Context, error) {
		transfer, release, err := authority.registerBuiltImageTransfer(ctx, registry, grantID, time.Now().Add(time.Minute), func(context.Context) error {
			current, err := load()
			if err != nil {
				return err
			}
			return validateBuiltImageAuthority(current, authority, 4, 4)
		})
		if err == nil {
			t.Cleanup(release)
		}
		return transfer, err
	}
	initial, err := load()
	if err != nil {
		t.Fatal(err)
	}
	pull, err := admit("pull-grant", initial)
	if err != nil {
		t.Fatal(err)
	}
	push, err := admit("push-grant", initial)
	if err != nil {
		t.Fatal(err)
	}

	off := setBuiltPolicyForTest(t, db, false, 2, 1)
	cache.applyBuiltImagePolicy(off, registry)
	if !errors.Is(pull.Err(), context.Canceled) || !errors.Is(push.Err(), context.Canceled) || publicCanceled {
		t.Errorf("disable transfer results = pull %v, push %v, public canceled %t, want only built transfers canceled",
			pull.Err(), push.Err(), publicCanceled)
	}
	if got := cache.builtImageAvailability(); got != "disabled" {
		t.Errorf("builtImageAvailability after disable = %q, want disabled", got)
	}
	for _, grantID := range []string{"new-pull", "new-push"} {
		if _, err := admit(grantID, initial); !errors.Is(err, guest.ErrGuestUnauthorized) {
			t.Errorf("admission of %s after disable = %v, want guest authorization denial", grantID, err)
		}
	}

	on := setBuiltPolicyForTest(t, db, true, 3, 1)
	cache.applyBuiltImagePolicy(on, registry)
	if _, err := admit("old-generation", initial); !errors.Is(err, guest.ErrGuestUnauthorized) {
		t.Errorf("admission of old generation after re-enable = %v, want guest authorization denial", err)
	}
	current, err := load()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := admit("fresh-generation", current)
	if err != nil {
		t.Fatalf("admission of fresh generation after re-enable = %v, want success", err)
	}
	cache.applyBuiltImagePolicy(on, registry)
	cache.applyBuiltImagePolicy(off, registry)
	if state := cache.builtImageAvailability(); fresh.Err() != nil || publicCanceled || state != "available" {
		t.Errorf("replaying policy changed live connections or availability: new built transfer %v, public canceled %t, state %s",
			fresh.Err(), publicCanceled, state)
	}
}

// TestBuiltImagePurgeResumesAcrossRestart leaves a leased active closure and
// a temporarily unverified retired root pending, then retries after re-enable.
func TestBuiltImagePurgeResumesAcrossRestart(t *testing.T) {
	ctx := t.Context()
	db := seedBuiltImageAuthority(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	activePath := imagecache.DefaultPath(base, "")
	retiredPath := filepath.Join(base, "cache", "previous-registry")
	// This fixture exercises deletion rather than host disk pressure.
	settings := imagecache.Settings{Enabled: true, StoragePath: activePath, MaxBytes: imagecache.Bytes(1 << 20),
		VolumeHighPercent: 98, VolumeLowPercent: 97}
	configuration := imagecache.Configuration{Version: 1, Desired: settings,
		Effective: imagecache.EffectiveSettings{Version: 1, Settings: settings}, Activation: imagecache.Activation{Phase: "ready"},
		RetiredLocations: []imagecache.RetiredLocation{{StoragePath: retiredPath, Bytes: "0", RetiredAt: time.Now().UTC()}}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET image_cache_settings_json=$1", string(encoded)); err != nil {
		t.Fatal(err)
	}
	makeRoot := func(path string) *imagecache.Cache {
		t.Helper()
		rootSettings := settings
		rootSettings.StoragePath = path
		store, err := imagecache.OpenSettings(ctx, base, "", rootSettings, 0)
		if err != nil {
			t.Fatalf("OpenSettings(%s) = %v, want success", path, err)
		}
		return store
	}
	closeRoot := func(store *imagecache.Cache) {
		t.Helper()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	putBlob := func(store *imagecache.Cache, scope imagecache.BuiltScope, body []byte) digest.Digest {
		t.Helper()
		id := digest.FromBytes(body)
		if err := store.PutBuiltBlob(ctx, scope, id, int64(len(body)), bytes.NewReader(body)); err != nil {
			t.Fatalf("PutBuiltBlob(%+v, %s) = %v, want success", scope, id, err)
		}
		return id
	}
	writeFile := func(path string, body []byte) {
		t.Helper()
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write cache fixture %s: %v", path, err)
		}
	}
	old := imagecache.BuiltScope{Generation: 1, ID: "old-active"}
	retired := makeRoot(retiredPath)
	retiredScope := imagecache.BuiltScope{Generation: 1, ID: "old-retired"}
	retiredID := putBlob(retired, retiredScope, []byte("old retired staging"))
	retiredPublicBytes := []byte("retired digest also linked by the public cache")
	retiredPublicID := putBlob(retired, retiredScope, retiredPublicBytes)
	closeRoot(retired)
	retiredPublicLink := filepath.Join(retiredPath, "link-public-retired-purge-fixture")
	retiredPublicBlob := filepath.Join(retiredPath, "blob-"+retiredPublicID.String())
	writeFile(retiredPublicLink, []byte(retiredPublicID))
	closeRoot(makeRoot(activePath))

	platform := v1.Platform{OS: "linux", Architecture: "amd64"}
	configBytes, err := json.Marshal(v1.Image{Platform: platform, RootFS: v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{}}})
	if err != nil {
		t.Fatal(err)
	}
	configID := digest.FromBytes(configBytes)
	manifestBytes, err := json.Marshal(v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{MediaType: v1.MediaTypeImageConfig, Digest: configID, Size: int64(len(configBytes))}, Layers: []v1.Descriptor{}})
	if err != nil {
		t.Fatal(err)
	}
	// A persisted public link and its digest-addressed bytes reconstruct a
	// real pull-through reference when the shared store is opened.
	publicLink := filepath.Join(activePath, "link-public-purge-fixture")
	publicBlob := filepath.Join(activePath, "blob-"+configID.String())
	writeFile(publicLink, []byte(configID))
	writeFile(publicBlob, configBytes)

	cache := openBuiltPolicyRuntime(t, db, base)
	t.Cleanup(func() { _ = cache.Close() })
	putBlob(cache.active, old, configBytes)
	manifest, err := cache.active.PutBuiltManifest(ctx, old, v1.MediaTypeImageManifest, int64(len(manifestBytes)), bytes.NewReader(manifestBytes))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := cache.active.AcquireBuiltImage(old, manifest, platform)
	if err != nil {
		t.Fatalf("AcquireBuiltImage(%+v, %s) = %v, want a complete closure", old, manifest, err)
	}
	t.Cleanup(lease.Close)

	registry := &proxy.Registry{}
	cache.applyBuiltImagePolicy(setBuiltPolicyForTest(t, db, false, 2, 1), registry)
	retiredBlob := filepath.Join(retiredPath, "blob-"+retiredID.String())
	// An admitted upload can already hold the runtime read lock before it
	// reserves its objects. Wait for that admission to finish or be canceled.
	cache.mu.RLock()
	deferred := cache.reconcileBuiltImagePurge(ctx)
	cache.mu.RUnlock()
	if _, err := os.Stat(retiredBlob); err != nil || deferred != nil {
		t.Fatalf("purge during an admitted transfer = %v, retired payload = %v, want a deferred pass", deferred, err)
	}
	retiredMarker := filepath.Join(retiredPath, ".shiftbox-image-cache")
	writeFile(retiredMarker, []byte("temporarily unavailable ownership marker"))
	// Leaving either root incomplete must keep the durable cutoff for retry.
	_ = cache.reconcileBuiltImagePurge(ctx)
	requireBuiltPurgeCutoff(t, db, sql.NullInt64{Int64: 1, Valid: true})
	if _, exists := cache.active.BuiltCandidate(old); !exists {
		t.Fatal("purge removed a candidate still protected by an active lease")
	}
	lease.Close()
	_ = cache.reconcileBuiltImagePurge(ctx)
	requireBuiltPurgeCutoff(t, db, sql.NullInt64{Int64: 1, Valid: true})
	if _, err := os.Stat(retiredBlob); err != nil {
		t.Fatalf("retired payload after unverified cleanup = %v, want untouched bytes", err)
	}

	cache.applyBuiltImagePolicy(setBuiltPolicyForTest(t, db, true, 3, 1), registry)
	newer := imagecache.BuiltScope{Generation: 3, ID: "new-active"}
	newID := putBlob(cache.active, newer, []byte("published after re-enabling"))
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	cache = openBuiltPolicyRuntime(t, db, base)
	requireBuiltPurgeCutoff(t, db, sql.NullInt64{Int64: 1, Valid: true})
	cache.applyBuiltImagePolicy(setBuiltPolicyForTest(t, db, false, 4, 1), registry)
	cache.applyBuiltImagePolicy(setBuiltPolicyForTest(t, db, true, 5, 1), registry)
	current := imagecache.BuiltScope{Generation: 5, ID: "current-active"}
	currentID := putBlob(cache.active, current, []byte("published after re-enabling again"))
	writeFile(retiredMarker, nil)
	if err := cache.reconcileBuiltImagePurge(ctx); err != nil {
		t.Fatalf("reconcileBuiltImagePurge after restart = %v, want success", err)
	}
	requireBuiltPurgeCutoff(t, db, sql.NullInt64{Int64: 3, Valid: true})

	if _, exists := cache.active.BuiltBlobSize(newer, newID); !exists {
		t.Error("purge removed a generation ahead of the oldest pending cutoff")
	}
	if err := cache.reconcileBuiltImagePurge(ctx); err != nil {
		t.Fatalf("reconcileBuiltImagePurge through advanced cutoff = %v, want success", err)
	}
	requireBuiltPurgeCutoff(t, db, sql.NullInt64{})
	if _, exists := cache.active.BuiltBlobSize(newer, newID); exists {
		t.Error("purge left the second disabled generation after advancing the cutoff")
	}
	if _, exists := cache.active.BuiltBlobSize(current, currentID); !exists {
		t.Error("purge removed the active generation's link")
	}
	if _, exists := cache.active.BuiltCandidate(old); exists {
		t.Error("purge left a retired generation's candidate")
	}
	for _, path := range []string{retiredBlob, filepath.Join(activePath, "blob-"+manifest.String())} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat purged exclusive payload %s = %v, want absent", path, err)
		}
	}
	for path, want := range map[string][]byte{
		publicLink: []byte(configID), publicBlob: configBytes,
		retiredPublicLink: []byte(retiredPublicID), retiredPublicBlob: retiredPublicBytes,
	} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Errorf("public cache file %s after purge = %q, %v, want unchanged bytes", path, got, err)
		}
	}
}

// TestBuiltImagePurgeReapsExpiredStaging keeps live upload scopes and the
// current published scope when reclaiming expired staging after a restart.
func TestBuiltImagePurgeReapsExpiredStaging(t *testing.T) {
	ctx := t.Context()
	db := seedBuiltImageAuthority(t)
	base := t.TempDir()
	cache := openBuiltPolicyRuntime(t, db, base)
	t.Cleanup(func() { _ = cache.Close() })
	configuration, err := cache.configuration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings := configuration.Effective.Settings
	settings.VolumeHighPercent, settings.VolumeLowPercent = 98, 97
	cache.active.SetLimits(settings, 0)
	now := time.Now()
	const insert = `INSERT INTO built_image_attempts(id,installation_id,project_id,workspace_id,workspace_generation,
		operation_id,dispatch_version,target_os,target_arch,repository_url,config_path,config_sha256,policy_generation,
		source_authorization_version,kind,state,created_at,expires_at,storage_path,storage_generation,manifest_digest)
	VALUES($1,$2,'project-first','workspace-first',$3,'launch-first',$4,'linux','amd64',$5,$6,$7,1,4,
		'launch',$8,$9,$10,$11,1,$12)`
	tests := []struct {
		scope, state string
		expires      time.Time
		id           digest.Digest
		preserved    bool
	}{
		{scope: "expired-staging", state: "receiving", expires: now.Add(-time.Minute)},
		{scope: "live-upload", state: "admitted", expires: now.Add(time.Minute), preserved: true},
		{scope: "published-slot", state: "promoted", expires: now.Add(-time.Minute), preserved: true},
	}
	path := configuration.Effective.StoragePath
	configSHA256 := digest.FromString("persisted config fixture").Encoded()
	for i := range tests {
		attempt := &tests[i]
		content := []byte(attempt.scope)
		attempt.id = digest.FromBytes(content)
		scope := imagecache.BuiltScope{Generation: 1, ID: attempt.scope}
		if err := cache.active.PutBuiltBlob(ctx, scope, attempt.id, int64(len(content)), bytes.NewReader(content)); err != nil {
			t.Fatalf("PutBuiltBlob(%+v) = %v, want success", scope, err)
		}
		var manifest sql.NullString
		if attempt.state == "promoted" {
			manifest = sql.NullString{String: attempt.id.String(), Valid: true}
		}
		if _, err := db.ExecContext(ctx, insert, attempt.scope, builtAuthorityInstallation, builtAuthorityGeneration, builtAuthorityDispatch,
			builtAuthorityRepository, builtAuthorityConfig, configSHA256, attempt.state, builtImageTime(now.Add(-3*time.Minute)),
			builtImageTime(attempt.expires), path, manifest); err != nil {
			t.Fatalf("insert attempt %s: %v", attempt.scope, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO built_image_slots(installation_id,project_id,target_os,target_arch,repository_url,
		config_path,config_sha256,manifest_digest,policy_generation,storage_generation,storage_path,storage_scope,publication_kind,origin_revision,promoted_at)
	VALUES($1,'project-first','linux','amd64',$2,$3,$4,$5,1,1,$6,'published-slot','launch','revision',$7)`,
		builtAuthorityInstallation, builtAuthorityRepository, builtAuthorityConfig, configSHA256, tests[2].id.String(), path,
		builtImageTime(now.Add(-2*time.Minute))); err != nil {
		t.Fatalf("insert current slot: %v", err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	cache = openBuiltPolicyRuntime(t, db, base)
	if err := cache.reconcileBuiltImagePurge(ctx); err != nil {
		t.Fatalf("reconcileBuiltImagePurge expired staging = %v, want success", err)
	}
	for _, attempt := range tests {
		scope := imagecache.BuiltScope{Generation: 1, ID: attempt.scope}
		if _, exists := cache.active.BuiltBlobSize(scope, attempt.id); exists != attempt.preserved {
			t.Errorf("BuiltBlobSize(%+v) present = %t, want %t", scope, exists, attempt.preserved)
		}
		var exists bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM built_image_attempts WHERE id=$1)", attempt.scope).Scan(&exists)
		if err != nil || exists != attempt.preserved {
			t.Errorf("attempt %s stored = %t, %v, want %t", attempt.scope, exists, err, attempt.preserved)
		}
	}
}

func openBuiltPolicyRuntime(t *testing.T, db *sql.DB, base string) *imageCacheRuntime {
	t.Helper()
	cache, err := newImageCacheRuntime(t.Context(), db, base, "", "https://guest.test:8444", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func setBuiltPolicyForTest(t *testing.T, db *sql.DB, enabled bool, generation, cutoff int64) imagecache.BuiltPolicy {
	t.Helper()
	flag := 0
	if enabled {
		flag = 1
	}
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `UPDATE installations SET built_image_enabled=$1,built_image_version=$2,
		built_image_generation=$2,built_image_purge_cutoff=$3 WHERE singleton=1`, flag, generation, cutoff); err != nil {
		t.Fatalf("persist built policy (enabled %t, generation %d, cutoff %d): %v", enabled, generation, cutoff, err)
	}
	policy, err := imagecache.ReadBuiltPolicy(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func requireBuiltPurgeCutoff(t *testing.T, db *sql.DB, want sql.NullInt64) {
	t.Helper()
	policy, err := imagecache.ReadBuiltPolicy(t.Context(), db)
	if err != nil || policy.PurgeCutoff != want {
		t.Fatalf("durable purge cutoff = %v, %v, want %v", policy.PurgeCutoff, err, want)
	}
}
