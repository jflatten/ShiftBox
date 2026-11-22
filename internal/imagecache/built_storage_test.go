package imagecache

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// TestBuiltStorageAdmitsBytesAndLinksBeforeReading verifies that uploads cannot
// start writing until space for both the body and a scoped link is available.
func TestBuiltStorageAdmitsBytesAndLinksBeforeReading(t *testing.T) {
	body := strings.Repeat("x", 256)
	id := digest.FromString(body)
	cache := newBuiltStorageTestCache(t, t.TempDir(), int64(len(body)))
	source := &builtReadCounter{Reader: strings.NewReader(body)}
	scope := BuiltScope{Generation: 1, ID: "attempt-admission"}
	err := cache.PutBuiltBlob(t.Context(), scope, id, int64(len(body)), source)
	if !errors.Is(err, ErrBuiltStorageFull) || source.reads != 0 {
		t.Errorf("PutBuiltBlob when body alone fills budget = (%v, %d reads), want (%v, 0)", err, source.reads, ErrBuiltStorageFull)
	}
	if store := cache.store; store.used != 0 || store.pending != 0 || store.temporary != 0 {
		t.Errorf("storage after refused upload = (%d used, %d pending, %d temporary), want all zero", store.used, store.pending, store.temporary)
	}
}

// TestBuiltStorageRejectsInvalidOrInterruptedUpload verifies that size errors,
// mismatched bytes and disconnections leave neither scoped data nor a reservation.
func TestBuiltStorageRejectsInvalidOrInterruptedUpload(t *testing.T) {
	cache := newBuiltStorageTestCache(t, t.TempDir(), 8<<10)
	const body = "verified layer bytes"
	id := digest.FromString(body)
	scope := BuiltScope{Generation: 1, ID: "attempt-invalid"}
	for _, fixture := range []struct {
		name string
		id   digest.Digest
		size int64
		body io.Reader
		want error
	}{
		{name: "length-required", id: id, size: -1, body: strings.NewReader(body), want: ErrBuiltInvalid},
		{name: "invalid-digest", id: "sha256:broken", size: int64(len(body)), body: strings.NewReader(body), want: ErrBuiltInvalid},
		{name: "short-body", id: id, size: int64(len(body)), body: strings.NewReader(body[:4]), want: ErrBuiltInvalid},
		{name: "overlong-body", id: id, size: int64(len(body)), body: strings.NewReader(body + "extra"), want: ErrBuiltInvalid},
		{name: "digest-mismatch", id: id, size: int64(len(body)), body: strings.NewReader(strings.Repeat("z", len(body))), want: ErrBuiltInvalid},
		{name: "interrupted-stream", id: id, size: int64(len(body)), body: io.MultiReader(strings.NewReader(body[:4]), builtDisconnected{}), want: io.ErrClosedPipe},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			err := cache.PutBuiltBlob(t.Context(), scope, fixture.id, fixture.size, fixture.body)
			if !errors.Is(err, fixture.want) {
				t.Errorf("PutBuiltBlob(%s, %d) = %v, want %v", fixture.id, fixture.size, err, fixture.want)
			}
			if _, ok := cache.BuiltBlobSize(scope, id); ok {
				t.Error("invalid upload became visible in the attempt")
			}
			if store := cache.store; store.used != 0 || store.pending != 0 || store.temporary != 0 {
				t.Errorf("storage after failed upload = (%d used, %d pending, %d temporary), want all zero", store.used, store.pending, store.temporary)
			}
			entries, err := os.ReadDir(cache.store.path)
			if err != nil || len(entries) != 0 {
				t.Errorf("storage files after failed upload = %v (%v), want empty", entries, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cache.PutBuiltBlob(ctx, scope, id, int64(len(body)), strings.NewReader(body)); !errors.Is(err, context.Canceled) {
		t.Errorf("PutBuiltBlob with a canceled request = %v, want context.Canceled", err)
	}
	if err := cache.PutBuiltBlob(t.Context(), scope, id, int64(len(body)), strings.NewReader(body)); err != nil {
		t.Errorf("PutBuiltBlob after interrupted upload = %v, want success", err)
	}
}

// TestBuiltStorageReopensSharedObjects verifies durable staging and byte
// accounting without letting one attempt use a different attempt's links.
func TestBuiltStorageReopensSharedObjects(t *testing.T) {
	dir := t.TempDir()
	body := []byte(strings.Repeat("shared layer", 400))
	id := digest.FromBytes(body)
	scopeA := BuiltScope{Generation: 1, ID: "attempt-one"}
	scopeB := BuiltScope{Generation: 1, ID: "attempt-two"}
	var used int64
	t.Run("stage", func(t *testing.T) {
		cache := newBuiltStorageTestCache(t, dir, 16<<10)
		putBuiltStorageTestBlob(t, cache, scopeA, body)
		used = cache.store.used
		if used <= int64(len(body)) {
			t.Errorf("used after first upload = %d, want body plus scoped link bytes", used)
		}
		if _, ok := cache.BuiltBlobSize(scopeB, id); ok {
			t.Error("a second attempt could see a blob it never uploaded")
		}
		putBuiltStorageTestBlob(t, cache, scopeB, body)
		if delta := cache.store.used - used; delta <= 0 || delta >= int64(len(body)) {
			t.Errorf("used bytes for a second scoped link = %d, want link bytes and no second payload", delta)
		}
		used = cache.store.used
		if err := os.WriteFile(filepath.Join(dir, "incoming-abandoned"), []byte("half of an upload"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	cache := newBuiltStorageTestCache(t, dir, 16<<10)
	if got := cache.store.used; got != used {
		t.Errorf("used after restarting = %d, want %d", got, used)
	}
	if _, err := os.Stat(filepath.Join(dir, "incoming-abandoned")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("abandoned staging file after restarting = %v, want absent", err)
	}
	for _, scope := range []BuiltScope{scopeA, scopeB} {
		if size, ok := cache.BuiltBlobSize(scope, id); !ok || size != int64(len(body)) {
			t.Errorf("BuiltBlobSize(%+v, %s) after restart = (%d, %t), want (%d, true)", scope, id, size, ok, len(body))
		}
	}
	if complete, err := cache.DiscardBuilt(scopeA); err != nil || !complete {
		t.Fatalf("DiscardBuilt(%+v) = (%t, %v), want (true, nil)", scopeA, complete, err)
	}
	if _, ok := cache.BuiltBlobSize(scopeA, id); ok {
		t.Error("discarded staging still resolves in the original scope")
	}
	if _, ok := cache.BuiltBlobSize(scopeB, id); !ok {
		t.Error("discarding one staging scope removed the other scope's shared data")
	}
}

// TestBuiltStorageRequiresCompleteScopedPlatformClosure verifies that a
// candidate is ineligible when a descriptor is missing, mismatches its bytes,
// or names a platform other than the controller requested.
func TestBuiltStorageRequiresCompleteScopedPlatformClosure(t *testing.T) {
	cache := newBuiltStorageTestCache(t, t.TempDir(), 16<<10)
	fixture := newBuiltStorageTestImage(t)
	scope := BuiltScope{Generation: 1, ID: "attempt-incomplete"}
	other := BuiltScope{Generation: 1, ID: "attempt-other"}
	putBuiltStorageTestBlob(t, cache, scope, fixture.config)
	putBuiltStorageTestBlob(t, cache, other, fixture.layer)
	id := putBuiltStorageTestManifest(t, cache, scope, fixture.manifest)
	if lease, err := cache.AcquireBuiltImage(scope, id, fixture.platform); !errors.Is(err, ErrBuiltImageUnavailable) {
		if lease != nil {
			lease.Close()
		}
		t.Errorf("AcquireBuiltImage with layer uploaded to another scope = %v, want %v", err, ErrBuiltImageUnavailable)
	}
	putBuiltStorageTestBlob(t, cache, scope, fixture.layer)
	wrongPlatform := v1.Platform{OS: "linux", Architecture: "arm64"}
	if lease, err := cache.AcquireBuiltImage(scope, id, wrongPlatform); !errors.Is(err, ErrBuiltImageUnavailable) {
		if lease != nil {
			lease.Close()
		}
		t.Errorf("AcquireBuiltImage for arm64 with an amd64 config = %v, want %v", err, ErrBuiltImageUnavailable)
	}
	var invalid v1.Manifest
	if err := json.Unmarshal(fixture.manifest, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid.Layers[0].Size++
	manifestWithWrongSize, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	putBuiltStorageTestBlob(t, cache, other, fixture.config)
	badID := putBuiltStorageTestManifest(t, cache, other, manifestWithWrongSize)
	if lease, err := cache.AcquireBuiltImage(other, badID, fixture.platform); !errors.Is(err, ErrBuiltImageUnavailable) {
		if lease != nil {
			lease.Close()
		}
		t.Errorf("AcquireBuiltImage with a descriptor that misstates the layer size = %v, want %v", err, ErrBuiltImageUnavailable)
	}

	platform := fixture.platform
	lease, err := cache.AcquireBuiltImage(scope, id, platform)
	if err != nil {
		t.Fatalf("AcquireBuiltImage of complete matching candidate = %v, want success", err)
	}
	lease.Close()
	// Simulate the missing content that remains possible after eviction or
	// restoring the metadata without its cache volume.
	if err := os.Remove(filepath.Join(cache.store.path, blobName(digest.FromBytes(fixture.layer)))); err != nil {
		t.Fatal(err)
	}
	if lease, err := cache.AcquireBuiltImage(scope, id, platform); !errors.Is(err, ErrBuiltImageUnavailable) {
		if lease != nil {
			lease.Close()
		}
		t.Errorf("AcquireBuiltImage after losing a layer = %v, want %v", err, ErrBuiltImageUnavailable)
	}
}

// TestBuiltInventoryShowsPublishedLayers verifies that the operator inventory
// reports a project's published image and detects a missing scoped layer.
func TestBuiltInventoryShowsPublishedLayers(t *testing.T) {
	cache := newBuiltStorageTestCache(t, t.TempDir(), 16<<10)
	fixture := newBuiltStorageTestImage(t)
	scope := BuiltScope{Generation: 1, ID: "published-image"}
	putBuiltStorageTestBlob(t, cache, scope, fixture.config)
	id := putBuiltStorageTestManifest(t, cache, scope, fixture.manifest)
	added := time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)
	entries := []PublishedImage{{Project: "Python tools", Digest: id, Platform: fixture.platform, AddedAt: added, Scope: scope}}
	for _, want := range []string{"partial", "complete"} {
		items := cache.BuiltInventory(entries)
		if len(items) != 1 {
			t.Fatalf("BuiltInventory(%s) = %+v, want one published image", id, items)
		}
		got := items[0]
		if got.Reference != "Python tools" || got.Source != "devcontainer" || !got.AddedAt.Equal(added) || got.Availability != want {
			t.Errorf("BuiltInventory(%s) = %+v, want Python tools devcontainer added at %v and %s", id, got, added, want)
		}
		var wantSize *string
		if want == "complete" {
			wantSize = Bytes(int64(len(fixture.layer)))
		}
		if diff := cmp.Diff([]*string{wantSize}, got.LayerBytes); diff != "" {
			t.Errorf("BuiltInventory(%s, %s) layer sizes mismatch (-want +got):\n%s", id, want, diff)
		}
		if want == "partial" {
			putBuiltStorageTestBlob(t, cache, scope, fixture.layer)
		}
	}
}

// TestBuiltStorageLeasesPreventEvictionAndPurge verifies that an issued lease
// can still fetch its closure under later budget pressure or purge. Once
// released, purge reclaims exclusive bytes and keeps public and newer links.
func TestBuiltStorageLeasesPreventEvictionAndPurge(t *testing.T) {
	cache := newBuiltStorageTestCache(t, t.TempDir(), 16<<10)
	fixture := newBuiltStorageTestImage(t)
	old := BuiltScope{Generation: 3, ID: "attempt-published"}
	newer := BuiltScope{Generation: 4, ID: "attempt-reenabled"}
	configID := putBuiltStorageTestBlob(t, cache, old, fixture.config)
	layerID := putBuiltStorageTestBlob(t, cache, old, fixture.layer)
	manifestID := putBuiltStorageTestManifest(t, cache, old, fixture.manifest)
	const publicLink = "link-public-fixture"
	if !cache.store.put(publicLink, []byte(configID)) {
		t.Fatal("could not create an existing public reference to the shared config")
	}
	newBody := []byte("published after re-enabling")
	newID := putBuiltStorageTestBlob(t, cache, newer, newBody)
	lease, err := cache.AcquireBuiltImage(old, manifestID, fixture.platform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	if err := cache.FitBudget(t.Context(), 1, 0); err == nil {
		t.Error("FitBudget evicted files belonging to an active image lease")
	}
	// Ordinary eviction can reclaim other cache files. Restore these links
	// before testing targeted purge so they are present when the purge runs.
	if !cache.store.put(publicLink, []byte(configID)) && string(cache.store.read(publicLink, 128)) != configID.String() {
		t.Fatal("could not restore the fixture public link after budget pressure")
	}
	putBuiltStorageTestBlob(t, cache, newer, newBody)
	if complete, err := cache.PurgeBuiltThrough(t.Context(), old.Generation); err != nil || complete {
		t.Errorf("PurgeBuiltThrough with an active lease = (%t, %v), want (false, nil)", complete, err)
	}
	if reader, _, err := lease.Open(newID); !errors.Is(err, ErrBuiltImageUnavailable) {
		if reader != nil {
			reader.Close()
		}
		t.Errorf("lease.Open on an unrelated object = %v, want %v", err, ErrBuiltImageUnavailable)
	}
	reader, desc, err := lease.Open(layerID)
	if err != nil {
		t.Fatalf("Open leased layer after budget pressure and purge = %v, want success", err)
	}
	lease.Close()
	if complete, err := cache.PurgeBuiltThrough(t.Context(), old.Generation); err != nil || complete {
		t.Errorf("PurgeBuiltThrough with an open transfer after grant closure = (%t, %v), want (false, nil)", complete, err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(got, fixture.layer) || desc.Size != int64(len(fixture.layer)) {
		t.Errorf("read leased layer after purge = (%d bytes, %v, descriptor %+v), want original %d-byte layer", len(got), err, desc, len(fixture.layer))
	}
	if complete, err := cache.PurgeBuiltThrough(t.Context(), old.Generation); err != nil || !complete {
		t.Fatalf("PurgeBuiltThrough after releasing the lease = (%t, %v), want (true, nil)", complete, err)
	}
	if _, err := os.Stat(filepath.Join(cache.store.path, blobName(layerID))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("purged exclusive layer after lease release = %v, want absent", err)
	}
	if got := cache.store.blob(publicLink, configID); got == nil {
		t.Error("purge lost a blob still linked from the public cache")
	} else {
		got.close()
	}
	if _, ok := cache.BuiltBlobSize(newer, newID); !ok {
		t.Error("purge removed a newer generation's staging link")
	}
	if _, ok := cache.BuiltCandidate(old); ok {
		t.Error("purge left the old candidate manifest visible")
	}
}

type builtReadCounter struct {
	io.Reader
	reads int
}

func (r *builtReadCounter) Read(data []byte) (int, error) {
	r.reads++
	return r.Reader.Read(data)
}

type builtDisconnected struct{}

func (builtDisconnected) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func newBuiltStorageTestCache(t *testing.T, dir string, budget int64) *Cache {
	t.Helper()
	cache, err := New(dir, budget)
	if err != nil {
		t.Fatal(err)
	}
	cache.store.volume = func() (int64, int64) { return 1 << 20, 1 << 20 }
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("closing test cache: %v", err)
		}
	})
	return cache
}

type builtStorageTestImage struct {
	manifest, config, layer []byte
	platform                v1.Platform
}

func newBuiltStorageTestImage(t *testing.T) builtStorageTestImage {
	t.Helper()
	var data bytes.Buffer
	archive := tar.NewWriter(&data)
	const file = "built layer fixture"
	if err := archive.WriteHeader(&tar.Header{Name: "fixture", Mode: 0o644, Size: int64(len(file))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, file); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	layer := data.Bytes()
	platform := v1.Platform{OS: "linux", Architecture: "amd64"}
	config, err := json.Marshal(v1.Image{Platform: platform, RootFS: v1.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromBytes(layer)}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(v1.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{Digest: digest.FromBytes(config), Size: int64(len(config)), MediaType: v1.MediaTypeImageConfig},
		Layers: []v1.Descriptor{{Digest: digest.FromBytes(layer), Size: int64(len(layer)), MediaType: v1.MediaTypeImageLayer}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return builtStorageTestImage{manifest: manifest, config: config, layer: layer, platform: platform}
}

func putBuiltStorageTestBlob(t *testing.T, cache *Cache, scope BuiltScope, body []byte) digest.Digest {
	t.Helper()
	id := digest.FromBytes(body)
	if err := cache.PutBuiltBlob(t.Context(), scope, id, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("PutBuiltBlob(%+v, %s) = %v, want success", scope, id, err)
	}
	return id
}

func putBuiltStorageTestManifest(t *testing.T, cache *Cache, scope BuiltScope, body []byte) digest.Digest {
	t.Helper()
	id, err := cache.PutBuiltManifest(t.Context(), scope, v1.MediaTypeImageManifest, int64(len(body)), bytes.NewReader(body))
	if want := digest.FromBytes(body); err != nil || id != want {
		t.Fatalf("PutBuiltManifest(%+v) = (%q, %v), want (%q, nil)", scope, id, err, want)
	}
	return id
}
