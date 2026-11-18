package imagecache

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	"golang.org/x/sys/unix"
)

type contentEntry struct {
	size     int64
	lastUsed time.Time
}

type contentStore struct {
	holdUntil                map[string]time.Time
	root                     *os.Root
	path                     string
	budget                   int64
	retired                  int64
	high, low                int64
	volume                   func() (total, available int64)
	mu                       sync.Mutex
	used, pending, temporary int64
	entries                  map[string]contentEntry
	readers                  map[string]int
	writing                  map[string]bool
	lastEviction             time.Time
	evicted                  int64
}

func openContentStore(path string, budget int64) (*contentStore, error) {
	store := &contentStore{path: path, budget: budget, high: 80, low: 70,
		holdUntil: make(map[string]time.Time), writing: make(map[string]bool), readers: make(map[string]int), entries: make(map[string]contentEntry)}
	store.volume = store.filesystem
	if path == "" && budget == 0 {
		return store, nil
	}
	if !filepath.IsAbs(path) || budget < 0 {
		return nil, errors.New("invalid cache storage settings")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	store.root = root
	dir, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	defer dir.Close()
	for {
		items, readErr := dir.ReadDir(128)
		for _, item := range items {
			name := item.Name()
			if strings.HasPrefix(name, "incoming-") && item.Type().IsRegular() {
				if err := root.Remove(name); err != nil {
					root.Close()
					return nil, err
				}
				continue
			}
			if !managedContent(name) || !item.Type().IsRegular() {
				continue
			}
			info, err := item.Info()
			if err != nil {
				root.Close()
				return nil, err
			}
			store.used += info.Size()
			store.entries[name] = contentEntry{info.Size(), info.ModTime()}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			root.Close()
			return nil, readErr
		}
	}
	return store, nil
}

func managedContent(name string) bool {
	return strings.HasPrefix(name, "blob-") || strings.HasPrefix(name, "manifest-") ||
		strings.HasPrefix(name, "link-") || strings.HasPrefix(name, "tag-")
}

func (s *contentStore) close() error {
	if s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *contentStore) read(name string, max int64) []byte {
	return s.readWithAccess(name, max, true)
}

func (s *contentStore) readWithAccess(name string, max int64, touch bool) []byte {
	entry := s.acquireWithAccess(name, touch)
	if entry == nil {
		return nil
	}
	defer entry.close()
	if entry.size > max {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(entry.file, max))
	if err != nil {
		return nil
	}
	return b
}

type localBlob struct {
	file    *os.File
	size    int64
	release func()
}

func (b *localBlob) close() {
	b.file.Close()
	b.release()
}

func blobName(id digest.Digest) string { return "blob-" + id.String() }

func (s *contentStore) acquire(name string) *localBlob {
	return s.acquireWithAccess(name, true)
}

func (s *contentStore) acquireWithAccess(name string, touch bool) *localBlob {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.entries[name]
	if !ok || s.root == nil {
		return nil
	}
	f, err := s.root.Open(name)
	if err != nil {
		return nil
	}
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != info.size {
		f.Close()
		return nil
	}
	if touch {
		info.lastUsed = time.Now()
		s.entries[name] = info
	}
	s.readers[name]++
	return &localBlob{file: f, size: stat.Size(), release: func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.readers[name]--
		if s.readers[name] == 0 {
			delete(s.readers, name)
		}
	}}
}

func (s *contentStore) blob(link string, id digest.Digest) *localBlob {
	return s.blobWithAccess(link, id, true)
}

func (s *contentStore) blobWithAccess(link string, id digest.Digest, touch bool) *localBlob {
	if string(s.readWithAccess(link, 128, touch)) != id.String() {
		return nil
	}
	return s.acquireWithAccess(blobName(id), touch)
}

type contentWrite struct {
	store *contentStore
	file  *os.File
	key   string
	temp  string
	size  int64
	wrote int64
}

func (s *contentStore) reserve(key string, size int64) *contentWrite {
	if s.root == nil || size <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if size > s.budget || s.writing[key] || s.readers[key] != 0 {
		return nil
	}
	if _, err := s.root.Lstat(key); !errors.Is(err, os.ErrNotExist) &&
		!(err == nil && strings.HasPrefix(key, "tag-") && s.entries[key].size > 0) {
		return nil
	}
	// After crossing the high watermark, make room down to low before
	// retaining anything new. Budget pressure alone only needs enough for
	// the object being admitted.
	total, available := s.volume()
	if total <= 0 || available < 0 || available > total {
		return nil
	}
	target := s.high
	if !s.volumeRoom(size, s.high) {
		target = s.low
	}
	for size > s.budget-s.retired-s.used-s.pending || !s.volumeRoom(size, target) {
		if !s.evictOldest(key) {
			return nil
		}
	}
	temp := "incoming-" + rand.Text()
	file, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil
	}
	s.writing[key] = true
	s.pending += size
	return &contentWrite{store: s, file: file, key: key, temp: temp, size: size}
}

// filesystem measures the mounted volume, including external writers.
func (s *contentStore) filesystem() (int64, int64) {
	var fs unix.Statfs_t
	if s.path == "" || unix.Statfs(s.path, &fs) != nil || fs.Blocks == 0 || fs.Bsize <= 0 {
		return 0, 0
	}
	return int64(fs.Blocks) * int64(fs.Bsize), int64(fs.Bavail) * int64(fs.Bsize)
}

// volumeRoom runs under the admission mutex. Temporary bytes are already
// reflected by statfs; count only the unwritten part of each reservation.
func (s *contentStore) volumeRoom(extra, threshold int64) bool {
	total, available := s.volume()
	if total <= 0 || available <= 0 || available > total {
		return false
	}
	headroom := total / 100 * (100 - threshold)
	return extra <= available-headroom-(s.pending-s.temporary)
}

func (s *contentStore) evictOldest(except string) bool {
	var oldest string
	var candidate contentEntry
	for name, entry := range s.entries {
		if name == except || s.readers[name] != 0 || s.writing[name] || time.Now().Before(s.holdUntil[name]) {
			continue
		}
		if oldest == "" || entry.lastUsed.Before(candidate.lastUsed) {
			oldest, candidate = name, entry
		}
	}
	if oldest == "" {
		return false
	}
	if err := s.root.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	delete(s.entries, oldest)
	delete(s.holdUntil, oldest)
	s.used -= candidate.size
	s.evicted += candidate.size
	s.lastEviction = time.Now()
	return true
}

func (w *contentWrite) write(data []byte) bool {
	s := w.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(data)) > w.size-w.wrote || !s.volumeRoom(0, s.high) {
		return false
	}
	n, err := w.file.Write(data)
	w.wrote += int64(n)
	s.temporary += int64(n)
	return err == nil && n == len(data)
}

func (w *contentWrite) finish(commit bool) bool {
	s := w.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := w.file.Close(); err != nil {
		commit = false
	}
	committed := commit && w.wrote == w.size && s.root.Rename(w.temp, w.key) == nil
	if committed {
		s.used += w.wrote - s.entries[w.key].size
		s.entries[w.key] = contentEntry{w.wrote, time.Now()}
	} else {
		if err := s.root.Remove(w.temp); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Count any abandoned bytes until startup reconciliation.
			s.used += w.wrote
		}
	}
	s.pending -= w.size
	s.temporary -= w.wrote
	delete(s.writing, w.key)
	return committed
}

func (s *contentStore) put(key string, data []byte) bool {
	w := s.reserve(key, int64(len(data)))
	if w == nil {
		return false
	}
	return w.finish(w.write(data))
}

// Stream keeps one chunk behind the network. A digest or length failure
// prevents the final bytes from being sent even when Content-Length is known.
func (s *contentStore) stream(ctx context.Context, dst http.ResponseWriter, src io.Reader, id digest.Digest, link string, size int64, stats *PullStats) error {
	verifier := id.Verifier()
	var pending *contentWrite
	if size <= s.budget-int64(len(id)) {
		pending = s.reserve(blobName(id), size)
	}
	var total int64
	retained := false
	defer func() {
		if pending != nil {
			pending.finish(false)
		}
		stats.UpstreamBytes += total
		if !retained {
			stats.StreamedBytes += total
		}
	}()
	previous := make([]byte, 0, 32<<10)
	current := make([]byte, 32<<10)
	sent := false
	send := func(data []byte) error {
		if len(data) == 0 {
			return nil
		}
		sent = true
		_, err := dst.Write(data)
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			if sent {
				panic(http.ErrAbortHandler)
			}
			return err
		}
		n, err := io.ReadFull(src, current)
		chunk := current[:n]
		total += int64(n)
		_, _ = verifier.Write(chunk)
		if pending != nil && !pending.write(chunk) {
			pending.finish(false)
			pending = nil
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if size >= 0 && total != size || !verifier.Verified() {
				if sent {
					panic(http.ErrAbortHandler)
				}
				dst.Header().Del("Content-Length")
				return corruptBlob(id)
			}
			if err := send(previous); err != nil {
				return err
			}
			if err := send(chunk); err != nil {
				return err
			}
			if pending != nil {
				retained = pending.finish(true)
				if retained {
					s.put(link, []byte(id))
				}
				pending = nil
			}
			return nil
		}
		if err != nil {
			if sent {
				panic(http.ErrAbortHandler)
			}
			dst.Header().Del("Content-Length")
			return err
		}
		if err := send(previous); err != nil {
			return err
		}
		previous, current = chunk, previous[:cap(previous)]
	}
}

// hold keeps an already complete stale image intact between the client's
// separate OCI manifest and layer requests. Live readers hold their own lease.
func (s *contentStore) hold(names []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		if _, ok := s.entries[name]; !ok {
			return false
		}
	}
	until := time.Now().Add(2 * time.Minute)
	for _, name := range names {
		s.holdUntil[name] = until
	}
	return true
}
