package imagecache

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
)

const (
	imageRecordLimit = 1024
	inventoryLimit   = 100
)

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

// Inventory checks the retained manifest and its repository links for up to
// 100 recent references. It neither contacts the upstream nor affects LRU order.
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
		if _, _, err := repo.cachedManifest(record.Digest); err != nil {
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
		availability := "partial"
		if repo.completeManifest(record.Digest, record.Architecture, 4, &names) {
			availability = "complete"
		}
		items = append(items, CachedImage{Reference: origin + "/" + record.Repository + separator + record.Target,
			Digest: record.Digest, Architecture: record.Architecture, Availability: availability})
	}
	return items
}

func (s *contentStore) inventoryNames() []string {
	type candidate struct {
		name string
		last time.Time
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make([]candidate, 0, inventoryLimit+1)
	for name, entry := range s.entries {
		if !strings.HasPrefix(name, "tag-") {
			continue
		}
		index := sort.Search(len(candidates), func(i int) bool {
			return candidates[i].last.Before(entry.lastUsed) || candidates[i].last.Equal(entry.lastUsed) && candidates[i].name > name
		})
		if index >= inventoryLimit {
			continue
		}
		if len(candidates) < inventoryLimit {
			candidates = append(candidates, candidate{})
		}
		copy(candidates[index+1:], candidates[index:])
		candidates[index] = candidate{name, entry.lastUsed}
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.name)
	}
	return names
}
