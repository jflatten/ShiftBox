package imagecache

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const imageRecordLimit = 1024

type imageRecord struct {
	Origin       string        `json:"origin"`
	Repository   string        `json:"repository"`
	Target       string        `json:"target"`
	Digest       digest.Digest `json:"digest"`
	Architecture string        `json:"architecture,omitempty"`
}

// CachedImage identifies a retained manifest and its observed content availability.
type CachedImage struct {
	Reference    string        `json:"reference"`
	Digest       digest.Digest `json:"digest"`
	Architecture string        `json:"architecture,omitempty"`
	Availability string        `json:"availability"`
	Source       string        `json:"source"`
	AddedAt      time.Time     `json:"addedAt"`
	LayerBytes   []*string     `json:"layerBytes"`
}

// PublishedImage identifies the project and storage scope of a promoted build.
type PublishedImage struct {
	Project  string
	Scope    BuiltScope
	Digest   digest.Digest
	Platform v1.Platform
	AddedAt  time.Time
}

func readImageRecord(data []byte) imageRecord {
	var record imageRecord
	if json.Unmarshal(data, &record) != nil {
		// Earlier cache tag files carried just the digest. They remain usable
		// for verified fallback but do not contain enough data for inventory.
		return imageRecord{Digest: digest.Digest(data)}
	}
	return record
}

func (r *repository) retainImageRecord(ctx context.Context, target string, id digest.Digest) {
	architecture, _ := ctx.Value(platformKey{}).(string)
	record := imageRecord{Origin: r.request.remote.origin.Host, Repository: r.request.remote.repository,
		Target: target, Digest: id, Architecture: architecture}
	data, err := json.Marshal(record)
	if err == nil && len(data) <= imageRecordLimit {
		r.cache.store.put(r.key("tag", target), data)
	}
}

// Inventory checks retained public manifests and repository links without
// contacting the upstream or affecting eviction order.
func (c *Cache) Inventory() []CachedImage {
	items := make([]CachedImage, 0)
	for _, name := range c.store.inventoryNames() {
		record := readImageRecord(c.store.readWithAccess(name, imageRecordLimit, false))
		if record.Origin == "" || record.Repository == "" || record.Target == "" || record.Digest.Validate() != nil {
			continue
		}
		repo := &repository{cache: c, inspect: true, request: &pullRequest{remote: remoteRegistry{
			origin: url.URL{Host: record.Origin}, repository: record.Repository,
		}}}
		if name != repo.key("tag", record.Target) {
			continue
		}
		manifestFile := repo.key("manifest", record.Digest.String())
		if _, _, err := repo.cachedManifest(record.Digest); err != nil {
			continue
		}
		info, err := c.store.root.Stat(manifestFile)
		if err != nil {
			continue
		}
		origin := record.Origin
		if origin == "registry-1.docker.io" {
			origin = "docker.io"
		}
		separator := ":"
		if digest.Digest(record.Target).Validate() == nil {
			separator = "@"
		}
		var names []string
		layers := make([]*string, 0)
		availability := "partial"
		if repo.completeManifest(record.Digest, record.Architecture, 4, &names, &layers) {
			availability = "complete"
		}
		items = append(items, CachedImage{Reference: origin + "/" + record.Repository + separator + record.Target,
			Digest: record.Digest, Architecture: record.Architecture, Availability: availability,
			Source: "registry", AddedAt: info.ModTime().UTC(), LayerBytes: layers})
	}
	return items
}

func (s *contentStore) inventoryNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for name := range s.entries {
		if strings.HasPrefix(name, "tag-") {
			names = append(names, name)
		}
	}
	return names
}

// BuiltInventory reports published builds whose manifests are still retained.
// Each layer is counted only if its link and expected bytes are in that scope.
func (c *Cache) BuiltInventory(published []PublishedImage) []CachedImage {
	var items []CachedImage
	for _, image := range published {
		prefix, ok := image.Scope.prefix()
		if !ok || !validBuiltDigest(image.Digest) {
			continue
		}
		candidate, ok := c.store.builtManifestDescriptor(prefix+"candidate", image.Digest)
		if !ok {
			continue
		}
		data := c.store.readWithAccess(blobName(image.Digest), manifestLimit, false)
		manifest, ok := decodeBuiltManifest(candidate.MediaType, data)
		if !ok || int64(len(data)) != candidate.Size || digest.FromBytes(data) != image.Digest {
			continue
		}
		layerBytes, complete := c.store.inspectBuiltReferences(prefix, manifest)
		availability := "partial"
		if complete && c.store.builtImagePlatform(manifest.Config, len(manifest.Layers), image.Platform, false) {
			availability = "complete"
		}
		items = append(items, CachedImage{Reference: image.Project, Digest: image.Digest, Architecture: image.Platform.Architecture,
			Availability: availability, Source: "devcontainer", AddedAt: image.AddedAt, LayerBytes: layerBytes})
	}
	return items
}

func (s *contentStore) inspectBuiltReferences(prefix string, manifest v1.Manifest) ([]*string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	configSize, ok := s.linkedBuiltSize(prefix+manifest.Config.Digest.String(), manifest.Config.Digest)
	complete := ok && configSize == manifest.Config.Size
	layerBytes := make([]*string, len(manifest.Layers))
	for i, layer := range manifest.Layers {
		size, ok := s.linkedBuiltSize(prefix+layer.Digest.String(), layer.Digest)
		if !ok || size != layer.Size {
			complete = false
			continue
		}
		layerBytes[i] = Bytes(size)
	}
	return layerBytes, complete
}
