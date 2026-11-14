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
