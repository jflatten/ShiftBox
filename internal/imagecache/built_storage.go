package imagecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/distribution/distribution/v3/manifest/schema2"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const builtLinkPrefix = "built-link-"

var (
	// ErrBuiltStorageFull means that the shared store could not retain an upload.
	ErrBuiltStorageFull = errors.New("built image cache has no capacity for this upload")
	// ErrBuiltInvalid means that the declared upload and its content do not agree.
	ErrBuiltInvalid = errors.New("invalid built image content")
	// ErrBuiltImageUnavailable means that a scoped image or object cannot be served.
	ErrBuiltImageUnavailable = errors.New("built image is unavailable")
)

// BuiltScope identifies one controller-authorized staging or published namespace.
// Its generation is the built-image policy generation, independent of storage settings.
type BuiltScope struct {
	Generation int64
	ID         string
}

func (s BuiltScope) prefix() (string, bool) {
	if s.Generation <= 0 || s.ID == "" || len(s.ID) > 128 {
		return "", false
	}
	return builtLinkPrefix + strconv.FormatInt(s.Generation, 10) + "." + digest.FromString(s.ID).Encoded() + ".", true
}

// PutBuiltBlob retains a sized, verified OCI blob under one scoped reference.
func (c *Cache) PutBuiltBlob(ctx context.Context, scope BuiltScope, id digest.Digest, size int64, src io.Reader) error {
	prefix, ok := scope.prefix()
	if !ok || size <= 0 || !validBuiltDigest(id) || src == nil {
		return ErrBuiltInvalid
	}
	return c.store.putBuiltObject(ctx, prefix+id.String(), id, "", size, src)
}

// BuiltBlobSize reports a present staged blob only when this scope owns a link.
func (c *Cache) BuiltBlobSize(scope BuiltScope, id digest.Digest) (int64, bool) {
	prefix, ok := scope.prefix()
	if !ok || !validBuiltDigest(id) {
		return 0, false
	}
	store := c.store
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.linkedBuiltSize(prefix+id.String(), id)
}

// PutBuiltManifest retains a single-platform image manifest under the scope's
// candidate tag. Closure validation happens separately when acquiring the image.
func (c *Cache) PutBuiltManifest(ctx context.Context, scope BuiltScope, mediaType string, size int64, src io.Reader) (digest.Digest, error) {
	prefix, ok := scope.prefix()
	mediaType, _, err := mime.ParseMediaType(mediaType)
	if !ok || err != nil || !builtManifestType(mediaType) || size <= 0 || size > manifestLimit || src == nil {
		return "", ErrBuiltInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(src, size+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != size {
		return "", ErrBuiltInvalid
	}
	if _, ok := decodeBuiltManifest(mediaType, data); !ok {
		return "", ErrBuiltInvalid
	}
	id := digest.FromBytes(data)
	if err := c.store.putBuiltObject(ctx, prefix+"candidate", id, mediaType, size, bytes.NewReader(data)); err != nil {
		return "", err
	}
	return id, nil
}

// BuiltCandidate reports the staged manifest digest for the scope's candidate tag.
func (c *Cache) BuiltCandidate(scope BuiltScope) (digest.Digest, bool) {
	prefix, ok := scope.prefix()
	if !ok {
		return "", false
	}
	store := c.store
	store.mu.Lock()
	defer store.mu.Unlock()
	name := prefix + "candidate"
	entry := store.entries[name]
	if _, ok := store.linkedBuiltSize(name, entry.target); !ok || !builtManifestType(entry.mediaType) {
		return "", false
	}
	return entry.target, true
}

// AcquireBuiltImage validates and pins the manifest, config and layers as one
// closure. The caller releases the lease when the authorized grant ends.
func (c *Cache) AcquireBuiltImage(scope BuiltScope, id digest.Digest, platform v1.Platform) (*BuiltImageLease, error) {
	prefix, ok := scope.prefix()
	if !ok || !validBuiltDigest(id) || platform.OS == "" || platform.Architecture == "" {
		return nil, ErrBuiltImageUnavailable
	}
	store := c.store
	candidate, ok := store.builtManifestDescriptor(prefix+"candidate", id)
	if !ok {
		return nil, ErrBuiltImageUnavailable
	}
	data := store.read(blobName(id), manifestLimit)
	manifest, ok := decodeBuiltManifest(candidate.MediaType, data)
	if !ok || digest.FromBytes(data) != id || int64(len(data)) != candidate.Size {
		return nil, ErrBuiltImageUnavailable
	}
	if !store.builtImagePlatform(manifest.Config, len(manifest.Layers), platform, true) {
		return nil, ErrBuiltImageUnavailable
	}
	return store.pinBuiltImage(prefix, candidate, manifest)
}

// BuiltImageLease keeps a verified image closure available across OCI requests.
// It may be used concurrently. Open readers retain their own pins when it closes.
type BuiltImageLease struct {
	store   *contentStore
	names   []string // Protected by store.mu. Nil after Close.
	objects map[digest.Digest]v1.Descriptor
}

// Open creates an independent reader for an object in the leased closure.
func (l *BuiltImageLease) Open(id digest.Digest) (io.ReadSeekCloser, v1.Descriptor, error) {
	store := l.store
	store.mu.Lock()
	defer store.mu.Unlock()
	desc, ok := l.objects[id]
	if !ok || l.names == nil {
		return nil, v1.Descriptor{}, ErrBuiltImageUnavailable
	}
	file := store.acquireLocked(blobName(id), true)
	if file == nil {
		return nil, v1.Descriptor{}, ErrBuiltImageUnavailable
	}
	return &builtObjectReader{File: file.file, release: file.release}, desc, nil
}

// Close releases the closure. In-progress reads keep their own leases.
func (l *BuiltImageLease) Close() {
	store := l.store
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, name := range l.names {
		store.unpin(name)
	}
	l.names = nil
}

type builtObjectReader struct {
	*os.File
	release func()
	once    sync.Once
}

func (r *builtObjectReader) Close() (err error) {
	r.once.Do(func() {
		err = r.File.Close()
		r.release()
	})
	return err
}

// DiscardBuilt reclaims a scope whose attempt has retired or expired. A false
// result means a reader or upload still holds its files and cleanup must retry.
func (c *Cache) DiscardBuilt(scope BuiltScope) (bool, error) {
	return c.store.discardBuilt(context.Background(), scope)
}

func (s *contentStore) discardBuilt(ctx context.Context, scope BuiltScope) (bool, error) {
	prefix, ok := scope.prefix()
	if !ok {
		return false, ErrBuiltInvalid
	}
	return s.purgeBuilt(ctx, func(name string) bool { return strings.HasPrefix(name, prefix) })
}

// PurgeBuiltThrough reclaims unleased scopes at or below the policy generation.
func (c *Cache) PurgeBuiltThrough(ctx context.Context, generation int64) (bool, error) {
	return c.store.purgeBuiltThrough(ctx, generation)
}

func (s *contentStore) purgeBuiltThrough(ctx context.Context, generation int64) (bool, error) {
	return s.purgeBuilt(ctx, func(name string) bool {
		raw, ok := strings.CutPrefix(name, builtLinkPrefix)
		if !ok {
			return false
		}
		value, _, _ := strings.Cut(raw, ".")
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && parsed > 0 && parsed <= generation
	})
}

// ReclaimBuilt cleans the disabled generations and expired staging in an open
// store. Active leases keep their scopes pending for another pass.
func (c *Cache) ReclaimBuilt(ctx context.Context, cutoff int64, expired []BuiltScope) (complete bool, reclaimed []BuiltScope, err error) {
	return c.store.reclaimBuilt(ctx, cutoff, expired)
}

// ReclaimManagedBuilt cleans an inactive managed root without creating it or
// enforcing a new storage budget. Public and newer links account for their
// shared bytes while the root is indexed.
func ReclaimManagedBuilt(ctx context.Context, installationDir, volumeID, path string, cutoff int64, expired []BuiltScope) (complete bool, reclaimed []BuiltScope, err error) {
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	settings := Settings{StoragePath: path, VolumeHighPercent: 80, VolumeLowPercent: 70}
	if err := CheckStoragePath(installationDir, volumeID, settings); err != nil {
		return false, nil, err
	}
	root, err := os.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) &&
		(within(filepath.Join(installationDir, "cache"), path) || within(VolumeRoot, path)) {
		// The installation directory or the verified dedicated volume is
		// accessible. An absent managed directory has no retained content.
		return true, expired, nil
	}
	if err != nil {
		return false, nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Lstat(storageMarker)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return false, nil, errors.New("managed cache root could not be verified for cleanup")
	}
	store, err := indexContentStore(root, path, 0, false)
	if err != nil {
		return false, nil, err
	}
	return store.reclaimBuilt(ctx, cutoff, expired)
}

func (s *contentStore) reclaimBuilt(ctx context.Context, cutoff int64, expired []BuiltScope) (bool, []BuiltScope, error) {
	complete := true
	var purgeErr error
	if cutoff > 0 {
		complete, purgeErr = s.purgeBuiltThrough(ctx, cutoff)
	}
	var reclaimed []BuiltScope
	for _, scope := range expired {
		if err := ctx.Err(); err != nil {
			return false, reclaimed, errors.Join(purgeErr, err)
		}
		if removed, err := s.discardBuilt(ctx, scope); err != nil {
			purgeErr = errors.Join(purgeErr, err)
		} else if removed {
			reclaimed = append(reclaimed, scope)
		}
	}
	return complete, reclaimed, purgeErr
}

func (s *contentStore) purgeBuilt(ctx context.Context, match func(string) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	complete := true
	for name := range s.writing {
		if match(name) {
			complete = false
		}
	}
	for name, entry := range s.entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !match(name) {
			continue
		}
		if s.entryBusy(name) || s.builtLinkBusy(name, entry) {
			complete = false
			continue
		}
		if err := s.removeWithBuiltObject(name); err != nil {
			return false, err
		}
	}
	return complete, nil
}

// builtWrite reserves the link and any new payload together. Existing files are
// pinned during the upload so eviction cannot separate a reference from its bytes.
type builtWrite struct {
	store      *contentStore
	blob, link *contentWrite
	pins       []string
}

func (s *contentStore) putBuiltObject(ctx context.Context, link string, id digest.Digest, mediaType string, size int64, src io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	linkData := []byte(id)
	if mediaType != "" {
		linkData = []byte(id.String() + "\n" + mediaType)
	}
	pending, err := s.reserveBuiltObject(link, id, mediaType, size, int64(len(linkData)))
	if err != nil {
		return err
	}
	defer pending.close()
	if err := receiveBuiltObject(ctx, src, id, size, pending.blob); err != nil {
		return err
	}
	if pending.link != nil && !pending.link.write(linkData) {
		return ErrBuiltStorageFull
	}
	return pending.commit(ctx)
}

func (s *contentStore) reserveBuiltObject(link string, id digest.Digest, mediaType string, size, linkSize int64) (*builtWrite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || s.budget <= 0 {
		return nil, ErrBuiltStorageFull
	}
	pending := &builtWrite{store: s}
	entry, exists := s.entries[link]
	hasLink := exists && s.regularEntry(link, entry.size)
	if hasLink {
		if entry.target != id || entry.mediaType != mediaType {
			return nil, ErrBuiltInvalid
		}
		s.pin(link, true)
		pending.pins = append(pending.pins, link)
	}
	blob := blobName(id)
	if entry, ok := s.entries[blob]; ok && s.regularEntry(blob, entry.size) {
		if entry.size != size {
			pending.closeLocked()
			return nil, ErrBuiltInvalid
		}
		s.pin(blob, true)
		pending.pins = append(pending.pins, blob)
	} else {
		pending.blob = s.reserveLocked(blob, size)
		if pending.blob == nil {
			pending.closeLocked()
			return nil, ErrBuiltStorageFull
		}
	}
	if !hasLink {
		pending.link = s.reserveLocked(link, linkSize)
		if pending.link == nil {
			pending.closeLocked()
			return nil, ErrBuiltStorageFull
		}
		pending.link.target, pending.link.mediaType = id, mediaType
	}
	return pending, nil
}

func receiveBuiltObject(ctx context.Context, src io.Reader, id digest.Digest, size int64, pending *contentWrite) error {
	verify := id.Verifier()
	var buffer [32 << 10]byte
	for remaining := size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buffer[:min(int64(len(buffer)), remaining)]
		n, err := io.ReadFull(src, chunk)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return ErrBuiltInvalid
			}
			return err
		}
		remaining -= int64(n)
		if _, err := verify.Write(chunk); err != nil {
			return err
		}
		if pending != nil && !pending.write(chunk) {
			return ErrBuiltStorageFull
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n, err := io.ReadFull(src, buffer[:1])
	if n != 0 || !verify.Verified() {
		return ErrBuiltInvalid
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (w *builtWrite) commit(ctx context.Context) error {
	s := w.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Install the link first. A crash between the renames leaves a dangling
	// link for the next retry or purge, with no untracked exclusive payload.
	var link string
	if w.link != nil {
		pending := w.link
		w.link = nil
		if !pending.finishLocked(true) {
			return ErrBuiltStorageFull
		}
		link = pending.key
	}
	if w.blob != nil {
		pending := w.blob
		w.blob = nil
		if !pending.finishLocked(true) {
			if link != "" {
				return errors.Join(ErrBuiltStorageFull, s.removeEntry(link))
			}
			return ErrBuiltStorageFull
		}
	}
	return nil
}

func (w *builtWrite) close() {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	w.closeLocked()
}

func (w *builtWrite) closeLocked() {
	if w.blob != nil {
		w.blob.finishLocked(false)
	}
	if w.link != nil {
		w.link.finishLocked(false)
	}
	for _, name := range w.pins {
		w.store.unpin(name)
	}
}

func (s *contentStore) regularEntry(name string, size int64) bool {
	if s.root == nil || size <= 0 {
		return false
	}
	info, err := s.root.Lstat(name)
	return err == nil && info.Mode().IsRegular() && info.Size() == size
}

func (s *contentStore) linkedBuiltSize(link string, id digest.Digest) (int64, bool) {
	entry, ok := s.entries[link]
	if !ok || id == "" || entry.target != id || !s.regularEntry(link, entry.size) {
		return 0, false
	}
	name := blobName(id)
	if object, ok := s.entries[name]; ok && s.regularEntry(name, object.size) {
		return object.size, true
	}
	return 0, false
}

func (s *contentStore) builtManifestDescriptor(link string, id digest.Digest) (v1.Descriptor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	size, ok := s.linkedBuiltSize(link, id)
	if !ok || size > manifestLimit {
		return v1.Descriptor{}, false
	}
	return v1.Descriptor{Digest: id, MediaType: s.entries[link].mediaType, Size: size}, true
}

func (s *contentStore) builtImagePlatform(config v1.Descriptor, layerCount int, platform v1.Platform, touch bool) bool {
	if config.Size > manifestLimit {
		return false
	}
	data := s.readWithAccess(blobName(config.Digest), manifestLimit, touch)
	var image v1.Image
	if int64(len(data)) != config.Size || digest.FromBytes(data) != config.Digest || json.Unmarshal(data, &image) != nil ||
		image.OS != platform.OS || image.Architecture != platform.Architecture || image.RootFS.Type != "layers" || len(image.RootFS.DiffIDs) != layerCount {
		return false
	}
	for _, id := range image.RootFS.DiffIDs {
		if !validBuiltDigest(id) {
			return false
		}
	}
	return true
}

func (s *contentStore) pinBuiltImage(prefix string, candidate v1.Descriptor, manifest v1.Manifest) (*BuiltImageLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	objects := make(map[digest.Digest]v1.Descriptor, len(manifest.Layers)+2)
	names := make([]string, 0, 2*(len(manifest.Layers)+2))
	add := func(link string, desc v1.Descriptor) bool {
		if previous, ok := objects[desc.Digest]; ok {
			return previous.Size == desc.Size && previous.MediaType == desc.MediaType
		}
		if size, ok := s.linkedBuiltSize(link, desc.Digest); !ok || size != desc.Size {
			return false
		}
		objects[desc.Digest] = v1.Descriptor{Digest: desc.Digest, Size: desc.Size, MediaType: desc.MediaType}
		names = append(names, link, blobName(desc.Digest))
		return true
	}
	if !add(prefix+"candidate", candidate) || !add(prefix+manifest.Config.Digest.String(), manifest.Config) {
		return nil, ErrBuiltImageUnavailable
	}
	for _, layer := range manifest.Layers {
		if !add(prefix+layer.Digest.String(), layer) {
			return nil, ErrBuiltImageUnavailable
		}
	}
	for _, name := range names {
		s.pin(name, true)
	}
	return &BuiltImageLease{store: s, names: names, objects: objects}, nil
}

func validBuiltDigest(id digest.Digest) bool {
	return id.Validate() == nil && id.Algorithm() == digest.SHA256
}

func builtManifestType(mediaType string) bool {
	return mediaType == v1.MediaTypeImageManifest || mediaType == schema2.MediaTypeManifest
}

func decodeBuiltManifest(mediaType string, data []byte) (v1.Manifest, bool) {
	var manifest v1.Manifest
	if !builtManifestType(mediaType) || json.Unmarshal(data, &manifest) != nil || manifest.SchemaVersion != 2 ||
		(manifest.MediaType != "" && manifest.MediaType != mediaType) || manifest.ArtifactType != "" || manifest.Subject != nil ||
		!validBuiltDescriptor(manifest.Config) || (manifest.Config.MediaType != v1.MediaTypeImageConfig && manifest.Config.MediaType != schema2.MediaTypeImageConfig) {
		return v1.Manifest{}, false
	}
	for _, layer := range manifest.Layers {
		if !validBuiltDescriptor(layer) || !builtLayerType(layer.MediaType) {
			return v1.Manifest{}, false
		}
	}
	return manifest, true
}

func validBuiltDescriptor(desc v1.Descriptor) bool {
	return validBuiltDigest(desc.Digest) && desc.Size > 0 && len(desc.URLs) == 0 && len(desc.Data) == 0
}

func builtLayerType(mediaType string) bool {
	switch mediaType {
	case v1.MediaTypeImageLayer, v1.MediaTypeImageLayerGzip, v1.MediaTypeImageLayerZstd, schema2.MediaTypeLayer:
		return true
	default:
		return false
	}
}
