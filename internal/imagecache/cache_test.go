package imagecache

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestLowerBudgetEvictsOnlyUnleasedContent verifies that a settings reduction
// waits for retained content in use and never deletes an unrelated file.
func TestLowerBudgetEvictsOnlyUnleasedContent(t *testing.T) {
	dir := t.TempDir()
	content := filepath.Join(dir, "blob-sha256:"+strings.Repeat("a", 64))
	unmanaged := filepath.Join(dir, "operator.txt")
	for _, path := range []string{content, unmanaged} {
		if err := os.WriteFile(path, []byte("12345678"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := New(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	lease := cache.store.acquire(filepath.Base(content))
	if lease == nil {
		t.Fatal("retained file was not admitted")
	}
	err = cache.FitBudget(t.Context(), 5, 2)
	lease.close()
	if err == nil {
		t.Fatal("budget reduction should fail while the retained file has a reader")
	}
	if err := cache.FitBudget(t.Context(), 5, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(content); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cache file after budget reduction = %v, want reclaimed", err)
	}
	if data, err := os.ReadFile(unmanaged); err != nil || string(data) != "12345678" {
		t.Errorf("operator file after budget reduction = %q, %v", data, err)
	}
}

// TestPublicTransportRejectsNonpublicDestinations verifies that registry, token,
// and redirected blob requests cannot dial private targets, even in a mixed DNS answer.
func TestPublicTransportRejectsNonpublicDestinations(t *testing.T) {
	for _, fixture := range []struct {
		name string
		url  string
		ips  []netip.Addr
	}{
		{name: "registry", url: "https://registry.example.net/v2/", ips: []netip.Addr{netip.MustParseAddr("10.20.30.40")}},
		{name: "auth_realm", url: "https://auth.example.net/token", ips: []netip.Addr{netip.MustParseAddr("fd00::2")}},
		{name: "blob_redirect", url: "https://blob.example.net/objects/layer", ips: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.1.10")}},
		{name: "loopback_literal", url: "https://127.0.0.1/v2/", ips: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{name: "plaintext", url: "http://registry.example.net/v2/", ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var dials atomic.Int64
			transport := newPublicTransport(
				func(context.Context, string, string) ([]netip.Addr, error) { return fixture.ips, nil },
				func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, errors.New("test dial sentinel")
				},
			)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fixture.url, nil)
			if err != nil {
				t.Fatalf("NewRequestWithContext(%q) error = %v, want valid request", fixture.url, err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				response.Body.Close()
			}
			if err == nil {
				t.Errorf("RoundTrip(%s) error = nil, want policy rejection", fixture.url)
			}
			if got := dials.Load(); got != 0 {
				t.Errorf("RoundTrip(%s) made %d dial attempts, want 0", fixture.url, got)
			}
		})
	}
}

// TestPublicTransportDialsValidatedAddress verifies that an allowed DNS answer
// is dialed as an IP, leaving no second hostname resolution at the connection boundary.
func TestPublicTransportDialsValidatedAddress(t *testing.T) {
	const registry = "registry.example.net"
	addresses := make(chan string, 1)
	transport := newPublicTransport(
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		func(_ context.Context, _ string, address string) (net.Conn, error) {
			select {
			case addresses <- address:
			default:
			}
			return nil, errors.New("test dial sentinel")
		},
	)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+registry+"/v2/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) error = %v, want valid request", registry, err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Errorf("RoundTrip(%s) error = nil, want dial sentinel error", request.URL)
	}
	select {
	case got := <-addresses:
		if want := "8.8.8.8:443"; got != want {
			t.Errorf("RoundTrip(%s) dialed %q, want %q", request.URL, got, want)
		}
	default:
		t.Errorf("RoundTrip(%s) made no dial attempt, want one pinned-IP dial", request.URL)
	}
}

// TestCacheStartsWithoutUpstreamAndRejectsNonPullRoutes verifies that an
// outage cannot block initialization and that push/catalog requests never reach upstream.
func TestCacheStartsWithoutUpstreamAndRejectsNonPullRoutes(t *testing.T) {
	root := t.TempDir()
	var upstreamCalls atomic.Int64
	cache, err := newCache(root, 1<<20, &http.Client{Transport: cacheRoundTripFunc(func(*http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		return nil, errors.New("upstream is unavailable")
	})})
	if err != nil {
		t.Fatalf("newCache(%q) error = %v, want successful local initialization", root, err)
	}
	for _, fixture := range []struct{ method, path string }{
		{method: http.MethodPost, path: "/v2/acme/widget/blobs/uploads/"},
		{method: http.MethodDelete, path: "/v2/acme/widget/manifests/latest"},
		{method: http.MethodGet, path: "/v2/_catalog"},
	} {
		recorder := httptest.NewRecorder()
		cache.ServeHTTP(recorder, httptest.NewRequest(fixture.method, fixture.path+"?ns=registry.example.net", nil))
		if got := recorder.Code; got != http.StatusNotFound && got != http.StatusMethodNotAllowed && got != http.StatusForbidden {
			t.Errorf("ServeHTTP(%s %s) status = %d, want pull-only rejection", fixture.method, fixture.path, got)
		}
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Errorf("initialization and non-pull requests made %d upstream requests, want 0", got)
	}
}

// TestCacheStreamsOversizedAnonymousBlob verifies that a public cross-host
// bearer challenge and blob redirect work without forwarding guest cookies or writing cache files.
func TestCacheStreamsOversizedAnonymousBlob(t *testing.T) {
	const registry = "registry.example.net"
	const realm = "auth.example.org"
	const blobHost = "blob.example.com"
	const body = "this layer is larger than the configured cache budget"
	const token = "anonymous-fixture-token"
	const budget = 16
	root := t.TempDir()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
	blobPath := "/v2/acme/widget/blobs/" + digest
	var initialFiles []string
	var requestedToken, readBlob atomic.Bool

	client := &http.Client{Transport: cacheRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Cookie"); got != "" {
			t.Errorf("upstream %s received Cookie %q, want no guest cookie", request.URL.Host, got)
		}
		if got := request.URL.Query().Get("ns"); got != "" {
			t.Errorf("upstream %s received mirror namespace %q, want no ns parameter", request.URL.Host, got)
		}
		switch request.URL.Host {
		case registry:
			auth := request.Header.Get("Authorization")
			if request.URL.Path != "/v2/" && request.URL.Path != blobPath {
				return nil, fmt.Errorf("unexpected registry path %q", request.URL.Path)
			}
			if auth != "Bearer "+token {
				response := cacheResponse(request, http.StatusUnauthorized, "")
				response.Header.Set("Www-Authenticate", `Bearer realm="https://`+realm+`/token",service="`+registry+`",scope="repository:acme/widget:pull"`)
				return response, nil
			}
			response := cacheResponse(request, http.StatusTemporaryRedirect, "")
			response.Header.Set("Location", "https://"+blobHost+"/layer")
			return response, nil
		case realm:
			requestedToken.Store(true)
			if got := request.Header.Get("Authorization"); got != "" {
				t.Errorf("token realm received Authorization %q, want anonymous request", got)
			}
			if got := request.URL.Query().Get("scope"); got != "repository:acme/widget:pull" {
				t.Errorf("token scope = %q, want pull-only scope for acme/widget", got)
			}
			response := cacheResponse(request, http.StatusOK, `{"token":"`+token+`","expires_in":300}`)
			response.Header.Set("Content-Type", "application/json")
			return response, nil
		case blobHost:
			if got := request.Header.Get("Authorization"); got != "" {
				t.Errorf("blob redirect received Authorization %q, want none across origins", got)
			}
			response := cacheResponse(request, http.StatusOK, body)
			response.Header.Set("Content-Type", "application/octet-stream")
			response.Header.Set("Docker-Content-Digest", digest)
			if request.Method == http.MethodGet {
				response.Body = &cacheObservedReader{
					ReadCloser: response.Body,
					observe: func() {
						readBlob.Store(true)
						if got := cacheRegularFiles(t, root); !slices.Equal(got, initialFiles) {
							t.Errorf("cache files during oversized blob stream = %v, want %v", got, initialFiles)
						}
					},
				}
			}
			return response, nil
		default:
			return nil, fmt.Errorf("unexpected upstream host %q", request.URL.Host)
		}
	})}
	cache, err := newCache(root, budget, client)
	if err != nil {
		t.Fatalf("newCache(%q, %d) error = %v, want successful initialization", root, budget, err)
	}
	initialFiles = cacheRegularFiles(t, root)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, blobPath+"?ns="+registry, nil)
	request.Header.Set("Cookie", "session=guest-fixture")
	cache.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != body {
		t.Errorf("ServeHTTP(%s) = status %d, body %q, want status 200 and body %q", blobPath, recorder.Code, recorder.Body.String(), body)
	}
	if !requestedToken.Load() || !readBlob.Load() {
		t.Errorf("ServeHTTP(%s) token requested = %t, blob read = %t, want both true", blobPath, requestedToken.Load(), readBlob.Load())
	}
	if got := cacheRegularFiles(t, root); !slices.Equal(got, initialFiles) {
		t.Errorf("cache files after oversized blob stream = %v, want %v", got, initialFiles)
	}
}

type cacheRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cacheRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func cacheResponse(request *http.Request, status int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	response := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Header:        header,
		Body:          http.NoBody,
		ContentLength: int64(len(body)),
		Request:       request,
	}
	if request.Method != http.MethodHead && body != "" {
		response.Body = io.NopCloser(strings.NewReader(body))
	}
	return response
}

type cacheObservedReader struct {
	io.ReadCloser
	observe func()
}

func (reader *cacheObservedReader) Read(p []byte) (int, error) {
	reader.observe()
	return reader.ReadCloser.Read(p)
}

func cacheRegularFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Errorf("WalkDir(%q) error = %v, want readable cache directory", root, err)
	}
	return files
}

// TestContentAdmissionEvictsBeforeWriting verifies that an active reader
// survives concurrent pressure and only the unleased oldest object is evicted.
func TestContentAdmissionEvictsBeforeWriting(t *testing.T) {
	store, err := openContentStore(t.TempDir(), 24)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	store.volume = func() (total, available int64) { return 1 << 20, 1 << 20 }
	old, active, next := "manifest-old", "manifest-active", "manifest-next"
	if !store.put(old, []byte("0123456789")) || !store.put(active, []byte("0123456789")) {
		t.Fatal("could not populate fixture")
	}
	file := store.acquire(active)
	if file == nil {
		t.Fatal("active fixture was unavailable")
	}
	defer file.close()
	lease := store.reserve(next, 12)
	if lease == nil {
		t.Fatal("could not reserve space by evicting an inactive object")
	}
	defer lease.finish(false)
	if got := store.read(old, 20); got != nil {
		t.Errorf("old entry remained after write reservation: %q", got)
	}
	if got := store.read(active, 20); string(got) != "0123456789" {
		t.Errorf("active entry = %q, want original bytes", got)
	}
	if other := store.reserve("manifest-concurrent", 4); other != nil {
		other.finish(false)
		t.Error("a concurrent write consumed space already reserved for the first writer")
	}
}

// TestControllerByteShareIgnoresMetadata verifies that checking a tag alone
// leaves a payload-only cache hit ratio unchanged.
func TestControllerByteShareIgnoresMetadata(t *testing.T) {
	stats := PullStats{CachedBytes: 30, UpstreamBytes: 10, MetadataBytes: 900, StreamedBytes: 10}
	if p := stats.ReusePercent(); p == nil || *p != 75 {
		t.Errorf("ReusePercent(%+v) = %v, want 75%%", stats, p)
	}
	if p := (PullStats{MetadataBytes: 500}).ReusePercent(); p != nil {
		t.Errorf("metadata-only ReusePercent = %v, want undefined", *p)
	}
}

// TestCacheRevalidatesTagsAndReusesDigests ensures a cached tag cannot hide a
// registry update while a retained immutable manifest remains reusable.
func TestCacheRevalidatesTagsAndReusesDigests(t *testing.T) {
	const media = "application/vnd.oci.image.manifest.v1+json"
	manifest := func(size int) string {
		return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:%064x","size":%d},"layers":[]}`, media, size, size)
	}
	body := manifest(1)
	var calls int
	cache, err := newCache(t.TempDir(), 1<<20, &http.Client{Transport: cacheRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		response := cacheResponse(request, http.StatusOK, body)
		response.Header.Set("Content-Type", media)
		response.Header.Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))))
		return response, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.store.volume = func() (int64, int64) { return 1 << 30, 1 << 30 }
	pull := func(ref string) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v2/repo/manifests/"+ref+"?ns=registry.example.com", nil)
		req.Header.Set("Accept", media)
		cache.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("pull %s status = %d, want 200: %s", ref, recorder.Code, recorder.Body)
		}
		return recorder.Header().Get("Docker-Content-Digest")
	}
	first := pull("latest")
	body = manifest(2)
	second := pull("latest")
	if first == "" || first == second {
		t.Fatalf("tag digests = %q and %q, want changed digest", first, second)
	}
	if got := pull(first); got != first || calls != 2 {
		t.Errorf("immutable pull = %q / %d upstream calls, want %q / 2", got, calls, first)
	}
}

// TestInventoryChecksEmbeddedOCIConfig verifies that a retained attestation is
// complete when its embedded config is valid and its provenance blob is cached.
func TestInventoryChecksEmbeddedOCIConfig(t *testing.T) {
	const media = "application/vnd.oci.image.manifest.v1+json"
	const provenance = `{"predicateType":"https://slsa.dev/provenance/v1"}`
	provenanceDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(provenance)))
	missingLayerDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("unretained provenance")))
	configDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("{}")))
	for _, test := range []struct {
		name       string
		configData string
		configSize int
		layer      string
		want       string
	}{
		{name: "verified inline config", configData: `"e30="`, configSize: 2, layer: provenanceDigest, want: "complete"},
		{name: "missing inline config", configData: `null`, configSize: 2, layer: provenanceDigest, want: "partial"},
		{name: "wrong inline digest", configData: `"W10="`, configSize: 2, layer: provenanceDigest, want: "partial"},
		{name: "wrong inline size", configData: `"e30="`, configSize: 3, layer: provenanceDigest, want: "partial"},
		{name: "missing provenance blob", configData: `"e30="`, configSize: 2, layer: missingLayerDigest, want: "partial"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,
				"artifactType":"application/vnd.docker.attestation.manifest.v1+json",
				"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":%d,"data":%s},
				"layers":[{"mediaType":"application/vnd.in-toto+json","digest":%q,"size":%d}]}`,
				media, configDigest, test.configSize, test.configData, test.layer, len(provenance))
			manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body)))
			cache, err := newCache(t.TempDir(), 1<<20, &http.Client{Transport: cacheRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v2/repo/manifests/" + manifestDigest:
					response := cacheResponse(request, http.StatusOK, body)
					response.Header.Set("Content-Type", media)
					return response, nil
				case "/v2/repo/blobs/" + provenanceDigest:
					return cacheResponse(request, http.StatusOK, provenance), nil
				default:
					return nil, fmt.Errorf("unexpected upstream request for %s", request.URL.Path)
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cache.Close() })
			cache.store.volume = func() (int64, int64) { return 1 << 30, 1 << 30 }
			for _, path := range []string{"manifests/" + manifestDigest, "blobs/" + provenanceDigest} {
				request := httptest.NewRequest(http.MethodGet, "/v2/repo/"+path+"?ns=registry.example.com", nil)
				request.Header.Set("Accept", media)
				response := httptest.NewRecorder()
				cache.ServeHTTP(response, request.WithContext(WithPlatform(request.Context(), "amd64")))
				if response.Code != http.StatusOK {
					t.Fatalf("GET %s = %d, want 200", path, response.Code)
				}
			}
			if items := cache.Inventory(); len(items) != 1 || items[0].Availability != test.want {
				t.Errorf("Inventory(%s) = %+v, want one %s reference", manifestDigest, items, test.want)
			} else {
				item := items[0]
				if item.Source != "registry" || item.AddedAt.IsZero() {
					t.Errorf("Inventory(%s) source and date added = %q and %v, want registry and a retained date", manifestDigest, item.Source, item.AddedAt)
				}
				var wantSize *string
				if test.layer == provenanceDigest {
					wantSize = Bytes(int64(len(provenance)))
				}
				if diff := cmp.Diff([]*string{wantSize}, item.LayerBytes); diff != "" {
					t.Errorf("Inventory(%s) layer sizes mismatch (-want +got):\n%s", manifestDigest, diff)
				}
			}
		})
	}
}

// TestTagFallbackRequiresCompleteContentAndAvailability verifies that an
// outage can serve a complete saved image but a denied or incomplete pull cannot.
func TestTagFallbackRequiresCompleteContentAndAvailability(t *testing.T) {
	const media = "application/vnd.oci.image.manifest.v1+json"
	const content = "retained image config"
	blob := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))
	body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"%s","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[]}`, media, blob, len(content))
	var status = http.StatusOK
	var upstreamErr error
	cache, err := newCache(t.TempDir(), 1<<20, &http.Client{Transport: cacheRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if upstreamErr != nil {
			return nil, upstreamErr
		}
		if status != http.StatusOK {
			response := cacheResponse(request, status, "")
			if status == http.StatusTooManyRequests {
				response.Header.Set("Retry-After", "60")
			}
			return response, nil
		}
		if strings.Contains(request.URL.Path, "/blobs/") {
			return cacheResponse(request, http.StatusOK, content), nil
		}
		response := cacheResponse(request, http.StatusOK, body)
		response.Header.Set("Content-Type", media)
		return response, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.store.volume = func() (int64, int64) { return 1 << 30, 1 << 30 }
	var retryAfter string
	pull := func(path string) (int, Observation) {
		t.Helper()
		var evidence Observation
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v2/repo/"+path+"?ns=registry.example.com", nil)
		req.Header.Set("Accept", media)
		cache.ServeHTTP(recorder, req.WithContext(WithObservation(req.Context(), func(o Observation) { evidence = o })))
		retryAfter = recorder.Header().Get("Retry-After")
		return recorder.Code, evidence
	}
	if got, _ := pull("manifests/latest"); got != http.StatusOK {
		t.Fatalf("initial tag = %d, want 200", got)
	}
	if items := cache.Inventory(); len(items) != 1 || items[0].Reference != "registry.example.com/repo:latest" || items[0].Availability != "partial" {
		t.Errorf("inventory before payload retention = %+v, want one partial reference", items)
	}
	status = http.StatusServiceUnavailable
	if got, notice := pull("manifests/latest"); got == http.StatusOK || notice.Stale {
		t.Errorf("incomplete outage pull = %d / %+v, want failure", got, notice)
	}
	status = http.StatusOK
	if got, _ := pull("blobs/" + blob); got != http.StatusOK {
		t.Fatalf("populate config = %d, want 200", got)
	}
	if items := cache.Inventory(); len(items) != 1 || items[0].Availability != "complete" {
		t.Errorf("inventory after payload retention = %+v, want the complete reference", items)
	}
	status = http.StatusServiceUnavailable
	if got, notice := pull("manifests/latest"); got != http.StatusOK || !notice.Stale || notice.Verified {
		t.Errorf("complete outage pull = %d / %+v, want stale success", got, notice)
	}
	status = http.StatusForbidden
	if got, notice := pull("manifests/latest"); got == http.StatusOK || notice.Stale {
		t.Errorf("denied pull = %d / %+v, want denial", got, notice)
	}
	status = http.StatusTooManyRequests
	if got, notice := pull("manifests/latest"); got != http.StatusTooManyRequests || notice.Stale || retryAfter != "60" {
		t.Errorf("rate-limited pull = %d / %+v / retry %q, want 429 without stale fallback and upstream Retry-After", got, notice, retryAfter)
	}
	upstreamErr = &tls.CertificateVerificationError{Err: errors.New("invalid upstream certificate")}
	if got, notice := pull("manifests/latest"); got == http.StatusOK || notice.Stale {
		t.Errorf("TLS failure pull = %d / %+v, want failure", got, notice)
	}
	upstreamErr = nil
	status = http.StatusOK
	if got, notice := pull("manifests/latest"); got != http.StatusOK || notice.Stale || !notice.Verified {
		t.Errorf("recovered pull = %d / %+v, want verified success", got, notice)
	}
}

// TestWorkspaceFreshnessRecoveryDoesNotReplaceRunningImage verifies that
// successful upstream revalidation clears the warning while preserving the
// image evidence from startup.
func TestWorkspaceFreshnessRecoveryDoesNotReplaceRunningImage(t *testing.T) {
	var freshness WorkspaceFreshness
	fixture := Observation{Origin: "registry.example.net", Repository: "acme/widget", Reference: "latest", Digest: "sha256:cached", Stale: true}
	freshness.record(fixture, true)
	if !freshness.WarningActive || freshness.WarningMessage != StaleWarning {
		t.Fatalf("outage freshness = %+v, want persistent stale warning", freshness)
	}
	fixture.Digest, fixture.Stale, fixture.Verified = "sha256:upstream", false, true
	freshness.record(fixture, false)
	if freshness.WarningActive || !freshness.UpdateAvailable ||
		freshness.References[0].ResolvedDigest != "sha256:cached" ||
		freshness.References[0].VerifiedDigest != "sha256:upstream" {
		t.Errorf("recovered freshness = %+v, want old startup image and newer verified digest", freshness)
	}
}
