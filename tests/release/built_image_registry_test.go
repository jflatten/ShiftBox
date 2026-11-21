package release_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/handlers"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const builtRegistryProofRepo = "shiftbox-built/release-proof"

// TestBuiltImageRegistryPublicReadOnly checks that the production pull-through
// handler rejects upload negotiation and writes to the public route.
func TestBuiltImageRegistryPublicReadOnly(t *testing.T) {
	cache := newBuiltRegistryPublicCache(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		const path = "/v2/library/proof/blobs/uploads/?ns=docker.io"
		response := httptest.NewRecorder()
		cache.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("public cache %s %s returned (%d, %q), want (405, %q)", method, path,
				response.Code, response.Header().Get("Allow"), "GET, HEAD")
		}
	}
}

// TestBuiltImageRegistryPinnedTransport records the native single-platform push
// and cold digest pull on a disposable Linux guest. The two operations must use
// the direct private namespace and the leaf configured when each operation runs.
func TestBuiltImageRegistryPinnedTransport(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_BUILT_IMAGE_REGISTRY") != "1" {
		t.Skip("set SHIFTBOX_TEST_BUILT_IMAGE_REGISTRY=1 on a disposable guest with the pinned toolchain")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("the registry proof requires root on a disposable Linux guest")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dockerConfig := t.TempDir()
	docker := func(ctx context.Context, input io.Reader, args ...string) (string, error) {
		command := exec.CommandContext(ctx, "/usr/bin/docker", append([]string{"--host=unix:///var/run/docker.sock", "--config", dockerConfig}, args...)...)
		command.Stdin = input
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	run := func(input io.Reader, args ...string) string {
		t.Helper()
		out, err := docker(ctx, input, args...)
		if err != nil {
			t.Fatalf("docker %v = %v (%s), want success", args, err, out)
		}
		return out
	}
	const versions = `{{.Client.Version}} {{.Server.Version}} {{range .Server.Components}}{{if eq .Name "containerd"}}{{.Version}}{{end}}{{end}}`
	gotVersions := strings.Fields(run(nil, "version", "--format", versions))
	if len(gotVersions) != 3 || gotVersions[0] != "29.9.3" || gotVersions[1] != "29.9.3" || strings.TrimPrefix(gotVersions[2], "v") != "2.4.2" {
		t.Fatalf("Docker client/server and containerd versions = %v, want [29.9.3 29.9.3 2.4.2]", gotVersions)
	}
	var driverStatus [][2]string
	if err := json.Unmarshal([]byte(run(nil, "info", "--format", "{{json .DriverStatus}}")), &driverStatus); err != nil {
		t.Fatal(err)
	}
	var imageStore string
	for _, pair := range driverStatus {
		if pair[0] == "driver-type" {
			imageStore = pair[1]
		}
	}
	if imageStore != "io.containerd.snapshotter.v1" {
		t.Fatalf("Docker driver type = %q, want io.containerd.snapshotter.v1", imageStore)
	}
	t.Logf("pinned transport: Docker client %s, Docker server %s, containerd %s", gotVersions[0], gotVersions[1], gotVersions[2])

	fixture := newBuiltRegistry(t)
	assertBuiltRegistryRejectsMissingLeaf(t, fixture)
	host := fixture.Listener.Addr().String()
	certificate, rotate := installBuiltRegistryHost(t, fixture, host)
	tag := host + "/" + builtRegistryProofRepo + ":candidate"
	var byDigest string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, ref := range []string{tag, byDigest} {
			if ref == "" {
				continue
			}
			if _, err := docker(cleanup, nil, "image", "inspect", ref); err == nil {
				if out, err := docker(cleanup, nil, "image", "rm", ref); err != nil {
					t.Errorf("removing fixture image %s: %v (%s)", ref, err, out)
				}
			}
		}
	})
	platform := "linux/" + runtime.GOARCH
	// Importing a unique rootfs avoids BuildKit cache references that could
	// satisfy the later pull without downloading any config or layer bytes.
	run(newBuiltRegistryRootfs(t, host), "image", "import", "--platform", platform, "-", tag)
	imageID := run(nil, "image", "inspect", "--format", "{{.Id}}", tag)
	run(nil, "image", "push", "--platform", platform, tag)
	push := fixture.drain(t)
	manifest := verifyBuiltRegistryPush(t, push, certificate)
	byDigest = host + "/" + builtRegistryProofRepo + "@" + manifest.String()
	run(nil, "image", "rm", tag)

	rotated := rotate()
	run(nil, "image", "pull", "--platform", platform, byDigest)
	pull := fixture.drain(t)
	verifyBuiltRegistryPull(t, pull, manifest, rotated, push)
	if got := run(nil, "image", "inspect", "--format", "{{.Id}}", byDigest); got != imageID {
		t.Errorf("pulled image ID = %s, want the pushed image ID %s", got, imageID)
	}

	missing := digest.FromString("unpublished release proof digest")
	if out, err := docker(ctx, nil, "image", "pull", "--platform", platform, host+"/"+builtRegistryProofRepo+"@"+missing.String()); err == nil {
		t.Errorf("pull of unpublished digest %s = %s, want failure", missing, out)
	}
	missingPath := "/v2/" + builtRegistryProofRepo + "/manifests/" + missing.String()
	failures := fixture.drain(t)
	if len(failures) == 0 || failures[0].path != missingPath || failures[0].status != http.StatusNotFound {
		t.Errorf("pull of unpublished digest %s recorded %v, want a direct 404", missing, failures)
	}
}

func assertBuiltRegistryRejectsMissingLeaf(t *testing.T, fixture builtRegistryTrace) {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(fixture.authorityPEM) {
		t.Fatal("could not read the fixture CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	if response, err := client.Get(fixture.URL + "/v2/"); err == nil {
		response.Body.Close()
		t.Fatalf("HTTPS probe without a client certificate returned %d, want TLS rejection", response.StatusCode)
	}
}

type builtRegistryRequest struct {
	method, path, digest, responseDigest, location, mediaType string
	serial, requestBytes, responseBytes                       int64
	status                                                    int
}

type builtRegistryTrace struct {
	*httptest.Server
	drain        func(*testing.T) []builtRegistryRequest
	authorityPEM []byte
	makeLeaf     func(int64) ([]byte, []byte)
}

func newBuiltRegistry(t *testing.T) builtRegistryTrace {
	t.Helper()
	public := newBuiltRegistryPublicCache(t)
	config := &configuration.Configuration{Storage: configuration.Storage{
		"inmemory":    {},
		"maintenance": {"uploadpurging": map[any]any{"enabled": false}},
	}}
	config.HTTP.RelativeURLs = true
	config.HTTP.Secret = "shiftbox-disposable-registry-proof"
	private := handlers.NewApp(t.Context(), config)
	t.Cleanup(func() {
		if err := private.Shutdown(); err != nil {
			t.Errorf("shutting down private fixture registry: %v", err)
		}
	})
	var mu sync.Mutex
	var recorded []builtRegistryRequest
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/"+builtRegistryProofRepo+"/") {
			public.ServeHTTP(w, r)
			return
		}
		if r.URL.Query().Has("ns") {
			http.Error(w, "private endpoint must be used directly", http.StatusBadRequest)
			return
		}
		result := &builtRegistryResponse{ResponseWriter: w, status: http.StatusOK}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		private.ServeHTTP(result, r)
		resultBytes, _ := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
		location := w.Header().Get("Location")
		// The upload state token is irrelevant to the adapter contract.
		location, _, _ = strings.Cut(location, "?")
		mu.Lock()
		recorded = append(recorded, builtRegistryRequest{
			method: r.Method, path: r.URL.Path, digest: r.URL.Query().Get("digest"),
			responseDigest: w.Header().Get("Docker-Content-Digest"), location: location, mediaType: r.Header.Get("Content-Type"),
			serial: r.TLS.PeerCertificates[0].SerialNumber.Int64(), requestBytes: r.ContentLength, responseBytes: resultBytes, status: result.status,
		})
		mu.Unlock()
	}))
	host, _, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		server.Listener.Close()
		t.Fatal(err)
	}
	authority, makeLeaf := builtRegistryTLS(t)
	leaf, err := authority.ServerLeaf([]string{host}, time.Now())
	if err != nil {
		server.Listener.Close()
		t.Fatal(err)
	}
	server.TLS = guest.ServerTLS(leaf, authority)
	server.StartTLS()
	t.Cleanup(server.Close)
	// Only the test process and Docker get the synthetic leaf files.
	authorityPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authority.Certificate.Raw})
	return builtRegistryTrace{Server: server, authorityPEM: authorityPEM, makeLeaf: makeLeaf, drain: func(t *testing.T) []builtRegistryRequest {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		out := recorded
		recorded = nil
		for _, request := range out {
			t.Logf("%s %s digest=%s content-length=%d -> %d bytes=%d digest=%s location=%s media-type=%s peer=%d",
				request.method, request.path, request.digest, request.requestBytes, request.status,
				request.responseBytes, request.responseDigest, request.location, request.mediaType, request.serial)
		}
		return out
	}}
}

type builtRegistryResponse struct {
	http.ResponseWriter
	status int
}

func (w *builtRegistryResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func newBuiltRegistryPublicCache(t *testing.T) *imagecache.Cache {
	t.Helper()
	cache, err := imagecache.New(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Errorf("shutting down public fixture cache: %v", err)
		}
	})
	return cache
}

func newBuiltRegistryRootfs(t *testing.T, unique string) io.Reader {
	t.Helper()
	var data bytes.Buffer
	archive := tar.NewWriter(&data)
	if err := archive.WriteHeader(&tar.Header{Name: "registry-proof", Mode: 0o644, Size: int64(len(unique))}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(archive, unique); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return &data
}

func builtRegistryTLS(t *testing.T) (*guest.Authority, func(serial int64) ([]byte, []byte)) {
	t.Helper()
	newKey := func() *ecdsa.PrivateKey {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	certPEM := func(template, issuer *x509.Certificate, key, signer *ecdsa.PrivateKey) []byte {
		t.Helper()
		body, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: body})
	}
	keyPEM := func(key *ecdsa.PrivateKey) []byte {
		t.Helper()
		body, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: body})
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ShiftBox release proof"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caKey := newKey()
	caPEM := certPEM(ca, ca, caKey, caKey)
	authority, err := guest.LoadCA(caPEM, keyPEM(caKey), now)
	if err != nil {
		t.Fatal(err)
	}
	return authority, func(serial int64) ([]byte, []byte) {
		t.Helper()
		key := newKey()
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
			URIs:     []*url.URL{{Scheme: "shiftbox", Host: "release-proof", Path: "/workspace/fixture/1"}},
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		return certPEM(leaf, authority.Certificate, key, caKey), keyPEM(key)
	}
}

func installBuiltRegistryHost(t *testing.T, fixture builtRegistryTrace, host string) (int64, func() int64) {
	t.Helper()
	const root = "/etc/docker/certs.d"
	if err := os.Mkdir(root, 0o755); err == nil {
		t.Cleanup(func() {
			if err := os.Remove(root); err != nil {
				t.Errorf("removing temporary Docker certificate root: %v", err)
			}
		})
	} else if !os.IsExist(err) {
		t.Fatal(err)
	}
	dir := filepath.Join(root, host)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("removing temporary Docker host configuration: %v", err)
		}
	})
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ca := filepath.Join(dir, "ca.pem")
	write(ca, fixture.authorityPEM)
	install := func(serial int64) {
		t.Helper()
		cert, key := fixture.makeLeaf(serial)
		certPath := filepath.Join(dir, fmt.Sprintf("client-%d.pem", serial))
		keyPath := filepath.Join(dir, fmt.Sprintf("client-%d.key", serial))
		write(certPath, cert)
		write(keyPath, key)
		next := filepath.Join(dir, "hosts.next")
		write(next, fmt.Appendf(nil, "server = %q\ncapabilities = [\"pull\", \"resolve\", \"push\"]\nca = %q\nclient = [[%q, %q]]\n",
			fixture.URL, ca, certPath, keyPath))
		if err := os.Rename(next, filepath.Join(dir, "hosts.toml")); err != nil {
			t.Fatal(err)
		}
	}
	const original, renewed = 2, 3
	install(original)
	return original, func() int64 {
		t.Helper()
		install(renewed)
		return renewed
	}
}

func verifyBuiltRegistryPush(t *testing.T, trace []builtRegistryRequest, serial int64) digest.Digest {
	t.Helper()
	const prefix = "/v2/" + builtRegistryProofRepo
	const manifestPath = prefix + "/manifests/candidate"
	opened := make(map[string]bool)
	missing := make(map[string]bool)
	var manifest digest.Digest
	var uploaded int
	for _, req := range trace {
		if req.serial != serial {
			t.Errorf("push %s %s used serial %d, want %d", req.method, req.path, req.serial, serial)
		}
		switch {
		case req.method == http.MethodHead && req.status == http.StatusNotFound:
			missing[req.path] = true
		case req.method == http.MethodPost && req.path == prefix+"/blobs/uploads/" && req.status == http.StatusAccepted && req.requestBytes == 0:
			opened[req.location] = true
		case req.method == http.MethodPut && opened[req.path]:
			if req.status != http.StatusCreated || req.requestBytes <= 0 || req.digest != req.responseDigest || !missing[prefix+"/blobs/"+req.digest] {
				t.Errorf("upload %s = %+v, want a previously missing digest committed with a positive Content-Length", req.path, req)
			}
			uploaded++
		case req.method == http.MethodPut && req.path == manifestPath:
			if !missing[manifestPath] || uploaded != 2 || req.status != http.StatusCreated || req.requestBytes <= 0 || !isBuiltRegistryImageManifest(req.mediaType) {
				t.Errorf("manifest write = %+v after %d uploads, want a sized single-platform manifest after config and layer uploads", req, uploaded)
			}
			manifest = digest.Digest(req.responseDigest)
		default:
			t.Errorf("unexpected private push request %+v", req)
		}
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("committed image digest = %q (%v), want a valid digest", manifest, err)
	}
	return manifest
}

func isBuiltRegistryImageManifest(mediaType string) bool {
	return mediaType == v1.MediaTypeImageManifest || mediaType == "application/vnd.docker.distribution.manifest.v2+json"
}

func verifyBuiltRegistryPull(t *testing.T, trace []builtRegistryRequest, manifest digest.Digest, serial int64, push []builtRegistryRequest) {
	t.Helper()
	const prefix = "/v2/" + builtRegistryProofRepo
	manifestPath := prefix + "/manifests/" + manifest.String()
	want := make(map[string]int64)
	for _, req := range push {
		if req.method != http.MethodPut {
			continue
		}
		if req.digest == "" {
			want[manifestPath] = req.requestBytes
		} else {
			want[prefix+"/blobs/"+req.digest] = req.requestBytes
		}
	}
	var resolved bool
	for _, req := range trace {
		if req.serial != serial {
			t.Errorf("pull %s %s used serial %d, want renewed serial %d", req.method, req.path, req.serial, serial)
		}
		if req.method == http.MethodHead && req.path == manifestPath && req.status == http.StatusOK {
			resolved = true
			continue
		}
		if req.method != http.MethodGet || req.status != http.StatusOK || req.responseBytes == 0 || req.responseBytes != want[req.path] {
			t.Errorf("cold digest pull request = %+v, want GET of an uploaded object with %d response bytes", req, want[req.path])
			continue
		}
		delete(want, req.path)
	}
	if !resolved || len(want) != 0 {
		t.Errorf("digest pull resolved manifest = %t, missing GETs = %v, want resolution and full closure over HTTPS", resolved, want)
	}
}
