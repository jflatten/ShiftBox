package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGuestListenersSeparateEnrollmentAndPolling proves that enrollment
// accepts CA-verified HTTPS and poll requires a client certificate at TLS.
func TestGuestListenersSeparateEnrollmentAndPolling(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
	ca, err := guest.LoadCA(pemCert, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), now)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	rotation := internalServerCertificate{authority: ca, name: "127.0.0.1", now: func() time.Time { return clock }}
	first, err := rotation.current(nil)
	if err != nil {
		t.Fatal(err)
	}
	clock = first.Leaf.NotBefore.Add(4*time.Hour - time.Second)
	retained, err := rotation.current(nil)
	if err != nil || retained != first {
		t.Fatalf("server leaf before rotation = %p, %v, want initial leaf", retained, err)
	}
	clock = clock.Add(time.Second)
	replacement, err := rotation.current(nil)
	if err != nil || replacement == first || bytes.Equal(replacement.Leaf.RawSubjectPublicKeyInfo, first.Leaf.RawSubjectPublicKeyInfo) {
		t.Fatalf("server leaf at rotation = %p, %v, want a new key", replacement, err)
	}
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keyStore, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	github, err := identity.NewGitHub(db, keyStore, "fixture-app", "fixture-secret", 1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := GuestListenerConfig{EnrollmentAddress: "127.0.0.1:0", PollAddress: "127.0.0.1:0", PublicHost: "127.0.0.1", GitHub: github,
		TunnelAddress: "127.0.0.1:0", Tunnels: &proxy.Tunnels{Registry: &proxy.Registry{}}}
	listeners, err := ListenGuests(cfg, guest.NewEnrollment(db, ca), ca, web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer listeners.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- listeners.Serve(ctx) }()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pemCert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	post := func(url string, want int) {
		t.Helper()
		resp, err := client.Post(url, "application/json", bytes.NewBufferString("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST %s = %d, want %d", url, resp.StatusCode, want)
		}
	}
	post(listeners.EnrollURL, 409)
	post(strings.Replace(listeners.EnrollURL, "/internal/v1/enroll", "/internal/v1/guest/poll", 1), 404)
	post(strings.Replace(listeners.EnrollURL, "/internal/v1/enroll", guest.GitAccessTokenPath, 1), 404)
	post(strings.Replace(listeners.EnrollURL, "/internal/v1/enroll", guest.RenewalPath, 1), 404)
	if resp, err := client.Post(listeners.PollURL, "application/json", bytes.NewBufferString("{}")); err == nil {
		resp.Body.Close()
		t.Error("poll listener accepted TLS without a client certificate")
	}
	if resp, err := client.Post(strings.Replace(listeners.PollURL, "/internal/v1/guest/poll", guest.GitAccessTokenPath, 1), "", nil); err == nil {
		resp.Body.Close()
		t.Error("Git token listener accepted TLS without a client certificate")
	}
	if resp, err := client.Post(strings.Replace(listeners.PollURL, "/internal/v1/guest/poll", guest.RenewalPath, 1), "", nil); err == nil {
		resp.Body.Close()
		t.Error("renewal listener accepted TLS without a client certificate")
	}
	if listeners.TunnelAddress == "" || strings.Contains(listeners.PollURL, listeners.TunnelAddress+"/") {
		t.Fatal("Yamux must use a distinct address from HTTPS polling")
	}
	if resp, err := client.Get("https://" + listeners.TunnelAddress); err == nil {
		resp.Body.Close()
		t.Error("tunnel accepted TLS without a client certificate")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Serve after cancellation = %v", err)
	}
}

// TestGuestImageCacheRequiresCurrentEnrollment proves a slow OCI response
// outlives the poll timeout, and revocation cancels a live pull and denies
// the same still CA-signed leaf on the next request.
func TestGuestImageCacheRequiresCurrentEnrollment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	const installation = "99999999-0000-4000-8000-000000000001"
	const workspace = "99999999-0000-4000-8000-000000000003"
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
			preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('`+installation+`','active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test',
			'{service}--{workspace}.preview.test','fixture','fixture','now','now',1);
		INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('owner','owner@example.test','member','active','local','setup','now','now',1);
		INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,
			owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('artifact','fixture','amd64','test','fixture','1','[]','{}','{}','{}',
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available');
		INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,
			observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES('`+workspace+`','`+installation+`','owner','template',1,'artifact','https://github.com/example/project',
			'devcontainer.json',2,1024,40,'{"mode":"shared"}','running','running',1,'owned','vm','now','now',1);
		INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('`+workspace+`',1,'eligible');
		INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
		VALUES('fixture-provision','`+workspace+`','provision',1,'owner','fixture-enrollment','running','now',1);
	`); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := EnsureInstallationCA(dir, installation, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := maintenanceGuestClient(t, db, ca, provider.WorkspaceRef{
		InstallationID: installation, WorkspaceID: workspace, Generation: 1, OwnershipMarker: "owned", InstanceID: "vm",
	})
	registry := &proxy.Registry{}
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	const manifestPath = "/v2/project/image/manifests/latest"
	const blobPath = "/v2/project/image/blobs/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cache := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case manifestPath:
			time.Sleep(150 * time.Millisecond)
			_, _ = io.WriteString(w, "manifest")
		case blobPath:
			_, _ = io.WriteString(w, "part of a blob")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush cache blob = %v, want success", err)
				return
			}
			started <- struct{}{}
			<-r.Context().Done()
			stopped <- struct{}{}
		default:
			t.Errorf("unexpected cache request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	const bind = "127.0.0.1:0"
	listeners, err := ListenGuests(GuestListenerConfig{
		EnrollmentAddress: bind, PollAddress: bind, TunnelAddress: bind, PublicHost: "127.0.0.1",
		Tunnels: &proxy.Tunnels{Registry: registry}, ImageCache: cache,
	}, guest.NewEnrollment(db, ca), ca, web.GuestCompatibility{Protocol: "1", CurrentRelease: "fixture"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(listeners.Close)
	// Shorten the ordinary control deadline to exercise the actual transfer
	// timeout behavior without waiting 20 seconds in a unit test.
	listeners.poll.server.WriteTimeout = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- listeners.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("guest listener shutdown = %v, want cancellation", err)
		}
	})
	const pollPath = "/internal/v1/guest/poll"
	const namespace = "?ns=mcr.microsoft.com"
	origin := strings.TrimSuffix(listeners.PollURL, pollPath)
	resp, err := client.Get(origin + manifestPath + namespace)
	if err != nil {
		t.Fatalf("GET slow manifest = %v, want successful response", err)
	}
	content, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(content) != "manifest" || readErr != nil {
		t.Fatalf("GET slow manifest = %d/%q/%v, want 200/manifest/nil", resp.StatusCode, content, readErr)
	}
	leaveOpen, err := client.Get(origin + blobPath + namespace)
	if err != nil {
		t.Fatalf("GET active blob = %v, want successful response", err)
	}
	t.Cleanup(func() { _ = leaveOpen.Body.Close() })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("GET active blob never reached the cache")
	}
	if _, err := db.ExecContext(ctx, "UPDATE guest_registrations SET state='revoked' WHERE workspace_id=$1", workspace); err != nil {
		t.Fatal(err)
	}
	if err := registry.CloseWorkspaceAccess(workspace, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("revocation did not promptly cancel the active blob request")
	}
	resp, err = client.Get(origin + manifestPath + namespace)
	if err != nil {
		t.Fatalf("GET after durable revocation = %v, want a denial response", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET after durable revocation = %d, want 401 or 403", resp.StatusCode)
	}
}
