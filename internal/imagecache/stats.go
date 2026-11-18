package imagecache

import (
	"context"
	"net/http"
	"time"
)

// PullStats reports compressed image bytes by source. Metadata is excluded
// from the image payload reuse percentage.
type PullStats struct {
	CachedBytes   int64 `json:"cachedBytes,string"`
	UpstreamBytes int64 `json:"upstreamBytes,string"`
	MetadataBytes int64 `json:"metadataBytes,string"`
	StreamedBytes int64 `json:"streamedBytes,string"`
}

// ReusePercent returns the percentage of image payload served by the cache.
// A metadata-only interval has no payload reuse rate.
func (s PullStats) ReusePercent() *float64 {
	if s.CachedBytes == 0 && s.UpstreamBytes == 0 {
		return nil
	}
	ratio := 100 * float64(s.CachedBytes) / (float64(s.CachedBytes) + float64(s.UpstreamBytes))
	return &ratio
}

func (s *PullStats) add(other PullStats) {
	s.CachedBytes += other.CachedBytes
	s.UpstreamBytes += other.UpstreamBytes
	s.MetadataBytes += other.MetadataBytes
	s.StreamedBytes += other.StreamedBytes
}

// Observation associates cache evidence with a single guest request.
type Observation struct {
	PullStats
	Origin, Repository, Reference, Digest string
	Verified, Stale                       bool
}

type observationKey struct{}

// WithObservation attaches an authenticated guest's observation callback.
func WithObservation(ctx context.Context, observe func(Observation)) context.Context {
	return context.WithValue(ctx, observationKey{}, observe)
}

// Effectiveness returns the byte totals and start time for this cache instance.
func (c *Cache) Effectiveness() (PullStats, time.Time) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.stats, c.started
}

type countResponse struct {
	http.ResponseWriter
	bytes *int64
}

func (w *countResponse) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	*w.bytes += int64(n)
	return n, err
}

type platformKey struct{}

// WithPlatform records the controller-authorized platform for stale image closure checks.
func WithPlatform(ctx context.Context, architecture string) context.Context {
	return context.WithValue(ctx, platformKey{}, architecture)
}
