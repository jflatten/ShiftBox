// Package imagecache embeds the Distribution read API with ShiftBox's
// public-only upstream access and bounded local storage.
package imagecache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/api/errcode"
	"github.com/distribution/distribution/v3/registry/handlers"
	registrymiddleware "github.com/distribution/distribution/v3/registry/middleware/registry"
	"github.com/distribution/distribution/v3/registry/storage/driver"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	middlewareName = "shiftbox-pull-cache"
	manifestLimit  = 4 << 20
)

var manifestAccept = strings.Join(distribution.ManifestMediaTypes(), ", ")

func init() {
	// Register the factory once. It receives the owning cache instance as
	// configuration. HTTP transports and storage remain instance-local.
	if err := registrymiddleware.Register(middlewareName, func(_ context.Context,
		base distribution.Namespace, _ driver.StorageDriver, options map[string]any,
	) (distribution.Namespace, error) {
		cache, ok := options["cache"].(*Cache)
		if !ok {
			return nil, errors.New("missing ShiftBox cache")
		}
		return &namespace{Namespace: base, cache: cache}, nil
	}); err != nil {
		panic(err)
	}
}

// Cache serves pull-only OCI requests. The caller authenticates guests before
// invoking it and closes it after stopping the guest listener.
type Cache struct {
	app     *handlers.App
	store   *contentStore
	client  *http.Client
	slots   chan struct{}
	statsMu sync.Mutex
	stats   PullStats
	started time.Time
}

// New opens the cache storage without contacting any upstream.
func New(root string, maxBytes int64) (*Cache, error) {
	return newCache(root, maxBytes, nil)
}

func newCache(root string, maxBytes int64, client *http.Client) (*Cache, error) {
	store, err := openContentStore(root, maxBytes)
	if err != nil {
		return nil, err
	}
	cache := &Cache{store: store, client: registryClient(client), slots: make(chan struct{}, 16), started: time.Now().UTC()}
	config := &configuration.Configuration{
		Storage: configuration.Storage{
			"inmemory": {},
			"maintenance": {
				"uploadpurging": map[any]any{"enabled": false},
				"readonly":      map[any]any{"enabled": true},
			},
		},
		Middleware: map[string][]configuration.Middleware{
			"registry": {{Name: middlewareName, Options: map[string]any{"cache": cache}}},
		},
	}
	// This fixed configuration registers no remote drivers or health checks.
	// Supply a fixed-length upload secret for initialization of the read-only
	// app. The outer router never exposes a handler that can use it.
	config.HTTP.Secret = "shiftbox-read-only-uploads-disabled"
	cache.app = handlers.NewApp(context.Background(), config)
	return cache, nil
}

// Close releases HTTP connections, the Distribution app and the storage root.
func (c *Cache) Close() error {
	c.client.CloseIdleConnections()
	return errors.Join(c.app.Shutdown(), c.store.close())
}

type pullKey struct{}

type pullRequest struct {
	remote      remoteRegistry
	request     *http.Request
	writer      http.ResponseWriter
	manifest    distribution.Manifest
	desc        v1.Descriptor
	blob        *http.Response
	local       *localBlob
	observation Observation
}

func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/v2/" {
		probe := r.Clone(r.Context())
		probe.URL.RawQuery, probe.URL.ForceQuery = "", false
		probe.RequestURI = probe.URL.RequestURI()
		c.app.ServeHTTP(w, probe)
		return
	}
	origin, repository, ok := pullTarget(r.URL)
	if !ok {
		http.NotFound(w, r)
		return
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		http.Error(w, "The image cache is busy", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	request := &pullRequest{
		remote:      remoteRegistry{origin: origin, repository: repository, client: c.client},
		request:     r,
		writer:      w,
		observation: Observation{Origin: origin.Host, Repository: repository},
	}
	defer func() {
		c.statsMu.Lock()
		c.stats.add(request.observation.PullStats)
		c.statsMu.Unlock()
		if observe, ok := ctx.Value(observationKey{}).(func(Observation)); ok {
			observe(request.observation)
		}
		if request.blob != nil {
			request.blob.Body.Close()
		}
		if request.local != nil {
			request.local.close()
		}
	}()
	pull := r.Clone(context.WithValue(ctx, pullKey{}, request))
	// Distribution includes the request URI in its logs. Only its validated
	// registry selector belongs there, regardless of guest-supplied extras.
	pull.URL.RawQuery = url.Values{"ns": {origin.Host}}.Encode()
	pull.URL.ForceQuery = false
	pull.RequestURI = pull.URL.RequestURI()
	c.app.ServeHTTP(w, pull)
}

func pullTarget(u *url.URL) (url.URL, string, bool) {
	var empty url.URL
	route, ok := strings.CutPrefix(u.Path, "/v2/")
	if !ok || u.EscapedPath() != u.Path {
		return empty, "", false
	}
	repoAndKind, object, ok := cutLastPath(route)
	repository, kind, kindFound := cutLastPath(repoAndKind)
	if !ok || !kindFound || len(repository) > reference.RepositoryNameTotalLengthMax || object == "" ||
		kind != "manifests" && kind != "blobs" {
		return empty, "", false
	}
	if _, err := reference.WithName(repository); err != nil {
		return empty, "", false
	}
	if kind == "blobs" {
		if _, err := digest.Parse(object); err != nil {
			return empty, "", false
		}
	} else if _, err := digest.Parse(object); err != nil {
		name, _ := reference.WithName(repository)
		if _, err := reference.WithTag(name, object); err != nil {
			return empty, "", false
		}
	}
	params, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(params["ns"]) != 1 {
		return empty, "", false
	}
	host := strings.ToLower(params.Get("ns"))
	switch host {
	case "docker.io", "index.docker.io":
		host = "registry-1.docker.io"
	}
	origin, err := url.Parse("https://" + host)
	if err != nil || origin.Host != host || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || !publicHTTPS(origin) {
		return empty, "", false
	}
	return *origin, repository, true
}

func cutLastPath(value string) (left, right string, ok bool) {
	index := strings.LastIndexByte(value, '/')
	if index == -1 {
		return "", "", false
	}
	return value[:index], value[index+1:], true
}

type namespace struct {
	distribution.Namespace
	cache *Cache
}

func (n *namespace) Repository(ctx context.Context, named reference.Named) (distribution.Repository, error) {
	request, ok := ctx.Value(pullKey{}).(*pullRequest)
	if !ok || named.Name() != request.remote.repository {
		return nil, errcode.ErrorCodeDenied.WithMessage("Unknown cache repository")
	}
	local, err := n.Namespace.Repository(ctx, named)
	if err != nil {
		return nil, err
	}
	return &repository{Repository: local, cache: n.cache, request: request}, nil
}

type repository struct {
	distribution.Repository
	cache   *Cache
	request *pullRequest
	inspect bool
}

func (r *repository) Manifests(ctx context.Context, opts ...distribution.ManifestServiceOption) (distribution.ManifestService, error) {
	local, err := r.Repository.Manifests(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &manifests{ManifestService: local, repo: r}, nil
}

func (r *repository) Tags(ctx context.Context) distribution.TagService {
	return &tags{TagService: r.Repository.Tags(ctx), repo: r}
}

func (r *repository) Blobs(ctx context.Context) distribution.BlobStore {
	return &blobs{BlobStore: r.Repository.Blobs(ctx), repo: r}
}

func (r *repository) key(kind, target string) string {
	return kind + "-" + digest.FromString(r.request.remote.origin.Host+"/"+r.request.remote.repository+"/"+target).Encoded()
}

func (r *repository) fetchManifest(ctx context.Context, target string) (distribution.Manifest, v1.Descriptor, error) {
	response, err := r.request.remote.get(ctx, http.MethodGet, "manifests", target, manifestAccept)
	if err != nil {
		return nil, v1.Descriptor{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, v1.Descriptor{}, upstreamError{response.StatusCode, response.Header.Get("Retry-After")}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, manifestLimit+1))
	r.request.observation.MetadataBytes += int64(len(data))
	if err != nil {
		return nil, v1.Descriptor{}, err
	}
	if len(data) > manifestLimit {
		return nil, v1.Descriptor{}, errors.New("registry manifest is too large")
	}
	declared := response.Header.Get("Docker-Content-Digest")
	if requested, err := digest.Parse(target); err == nil {
		declared = requested.String()
	}
	manifest, desc, err := decodeManifest(response.Header.Get("Content-Type"), data, declared)
	if err != nil {
		return nil, v1.Descriptor{}, err
	}
	return manifest, desc, nil
}

func (r *repository) upstreamManifest(ctx context.Context, target string) (distribution.Manifest, v1.Descriptor, error) {
	manifest, desc, err := r.fetchManifest(ctx, target)
	if err != nil {
		return nil, v1.Descriptor{}, err
	}
	_, data, err := manifest.Payload()
	if err != nil {
		return nil, v1.Descriptor{}, err
	}
	body := append([]byte(desc.MediaType+"\n"), data...)
	r.cache.store.put(r.key("manifest", desc.Digest.String()), body)
	return manifest, desc, nil
}

func decodeManifest(mediaType string, data []byte, declared string) (distribution.Manifest, v1.Descriptor, error) {
	mediaType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return nil, v1.Descriptor{}, errors.New("unsupported registry manifest media type")
	}
	manifest, desc, err := distribution.UnmarshalManifest(mediaType, data)
	if err != nil {
		return nil, v1.Descriptor{}, errors.New("invalid registry manifest")
	}
	if declared != "" {
		expected, err := digest.Parse(declared)
		if err != nil || expected.Algorithm().FromBytes(data) != expected {
			return nil, v1.Descriptor{}, errors.New("registry manifest digest does not match the requested image")
		}
		desc.Digest = expected
	}
	desc.MediaType = mediaType
	desc.Size = int64(len(data))
	return manifest, desc, nil
}

func protocolError(w http.ResponseWriter, err error, missing error) error {
	var status upstreamError
	if errors.As(err, &status) {
		switch status.status {
		case http.StatusNotFound:
			return missing
		case http.StatusUnauthorized, http.StatusForbidden:
			return errcode.ErrorCodeDenied.WithMessage("The upstream registry denied anonymous access")
		case http.StatusTooManyRequests:
			if status.retryAfter != "" {
				w.Header().Set("Retry-After", status.retryAfter)
			}
			return errcode.ErrorCodeTooManyRequests.WithMessage("The upstream registry rate limited the pull")
		}
	}
	return errcode.ErrorCodeUnavailable.WithMessage("The upstream image could not be fetched")
}

func (r *repository) cachedManifest(id digest.Digest) (distribution.Manifest, v1.Descriptor, error) {
	data := r.cache.store.readWithAccess(r.key("manifest", id.String()), manifestLimit+128, !r.inspect)
	contentType, body, ok := strings.Cut(string(data), "\n")
	if !ok {
		return nil, v1.Descriptor{}, errors.New("manifest is not retained")
	}
	return decodeManifest(contentType, []byte(body), id.String())
}

func (r *repository) completeManifest(id digest.Digest, arch string, depth int, names *[]string) bool {
	if depth == 0 || len(*names) > 256 {
		return false
	}
	manifest, _, err := r.cachedManifest(id)
	if err != nil {
		return false
	}
	*names = append(*names, r.key("manifest", id.String()))
	references := manifest.References()
	index := false
	for _, item := range references {
		if item.Platform != nil {
			index = true
			break
		}
	}
	if index {
		found := false
		for _, item := range references {
			if item.Platform == nil || item.Platform.OS != "linux" || item.Platform.Architecture != arch {
				continue
			}
			found = true
			if !r.completeManifest(item.Digest, arch, depth-1, names) {
				return false
			}
		}
		return found
	}
	for _, item := range references {
		link := r.key("link", item.Digest.String())
		if item.Digest.Validate() != nil {
			return false
		}
		file := r.cache.store.blobWithAccess(link, item.Digest, !r.inspect)
		if file == nil {
			return false
		}
		size := file.size
		file.close()
		if size != item.Size {
			return false
		}
		*names = append(*names, link, blobName(item.Digest))
	}
	return len(references) > 0
}

func (r *repository) staleManifest(ctx context.Context, tag string) (distribution.Manifest, v1.Descriptor, bool) {
	data := r.cache.store.read(r.key("tag", tag), imageRecordLimit)
	id := readImageRecord(data).Digest
	err := id.Validate()
	if err != nil {
		return nil, v1.Descriptor{}, false
	}
	arch, _ := ctx.Value(platformKey{}).(string)
	var names []string
	if !r.completeManifest(id, arch, 4, &names) || !r.cache.store.hold(names) {
		return nil, v1.Descriptor{}, false
	}
	manifest, desc, err := r.cachedManifest(id)
	return manifest, desc, err == nil
}

type tags struct {
	distribution.TagService
	repo *repository
}

func (t *tags) Get(ctx context.Context, tag string) (v1.Descriptor, error) {
	repo := t.repo
	request := repo.request
	manifest, desc, err := repo.upstreamManifest(ctx, tag)
	if err != nil {
		var ok bool
		if availabilityFailure(err) {
			manifest, desc, ok = repo.staleManifest(ctx, tag)
		}
		if !ok {
			return v1.Descriptor{}, protocolError(request.writer, err, distribution.ErrTagUnknown{Tag: tag})
		}
		request.observation.Stale = true
	} else {
		repo.retainImageRecord(ctx, tag, desc.Digest)
		request.observation.Verified = true
	}
	request.manifest, request.desc = manifest, desc
	request.observation.Reference = tag
	request.observation.Digest = desc.Digest.String()
	return desc, nil
}

type manifests struct {
	distribution.ManifestService
	repo *repository
}

func (m *manifests) Get(ctx context.Context, id digest.Digest, _ ...distribution.ManifestServiceOption) (distribution.Manifest, error) {
	if m.repo.request.manifest != nil && m.repo.request.desc.Digest == id {
		return m.repo.request.manifest, nil
	}
	if manifest, _, err := m.repo.cachedManifest(id); err == nil {
		m.repo.retainImageRecord(ctx, id.String(), id)
		return manifest, nil
	}
	manifest, _, err := m.repo.upstreamManifest(ctx, id.String())
	if err != nil {
		return nil, protocolError(m.repo.request.writer, err,
			distribution.ErrManifestUnknownRevision{Name: m.repo.Named().Name(), Revision: id})
	}
	m.repo.retainImageRecord(ctx, id.String(), id)
	return manifest, nil
}

type blobs struct {
	distribution.BlobStore
	repo *repository
}

func (b *blobs) Stat(ctx context.Context, id digest.Digest) (v1.Descriptor, error) {
	r := b.repo.request
	file := b.repo.cache.store.blob(b.repo.key("link", id.String()), id)
	if file != nil {
		r.local = file
		return v1.Descriptor{Digest: id, Size: file.size, MediaType: "application/octet-stream"}, nil
	}
	response, err := r.remote.get(ctx, r.request.Method, "blobs", id.String(), "")
	if err != nil {
		return v1.Descriptor{}, protocolError(r.writer, err, distribution.ErrBlobUnknown)
	}
	r.blob = response
	if response.StatusCode != http.StatusOK {
		return v1.Descriptor{}, protocolError(r.writer, upstreamError{response.StatusCode, response.Header.Get("Retry-After")}, distribution.ErrBlobUnknown)
	}
	// A positive response from this upstream authorizes a separate
	// repository link to already verified content with the same digest.
	if file := b.repo.cache.store.acquire(blobName(id)); file != nil {
		r.local = file
		b.repo.cache.store.put(b.repo.key("link", id.String()), []byte(id))
		return v1.Descriptor{Digest: id, Size: file.size, MediaType: "application/octet-stream"}, nil
	}
	return v1.Descriptor{Digest: id, Size: response.ContentLength, MediaType: "application/octet-stream"}, nil
}

func (b *blobs) ServeBlob(ctx context.Context, w http.ResponseWriter, request *http.Request, id digest.Digest) error {
	r := b.repo.request
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", id.String())
	w.Header().Set("ETag", strconv.Quote(id.String()))
	if r.local != nil {
		http.ServeContent(&countResponse{ResponseWriter: w, bytes: &r.observation.CachedBytes}, request, "", time.Time{}, r.local.file)
		return nil
	}
	response := r.blob
	if response == nil || response.ContentLength < 0 && request.Method == http.MethodHead {
		return errcode.ErrorCodeUnavailable.WithMessage("The upstream layer size is unavailable")
	}
	if response.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	if request.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	return b.repo.cache.store.stream(ctx, w, response.Body, id, b.repo.key("link", id.String()), response.ContentLength, &r.observation.PullStats)
}

func corruptBlob(id digest.Digest) error {
	return fmt.Errorf("upstream layer does not match %s", id)
}
