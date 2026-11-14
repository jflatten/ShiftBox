package guest_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGuestAgentEnrollmentRetry proves a fresh agent recovers the exact key
// across an enrollment failure and loads the issued identity without the seed.
func TestGuestAgentEnrollmentRetry(t *testing.T) {
	ca, authorityKeyPEM, issuerKey, issuer := authorityFixture(t)
	var attempted []byte
	var issued []byte
	now := time.Now()
	handler := web.GuestEnrollmentHandler(web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"}, func(_ context.Context, _ string, der []byte, _ time.Time) ([]byte, error) {
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			return nil, err
		}
		if attempted != nil {
			if !bytes.Equal(attempted, csr.RawSubjectPublicKeyInfo) {
				t.Error("retry used a different key")
				return nil, guest.ErrEnrollmentDenied
			}
			return issued, nil
		}
		attempted = csr.RawSubjectPublicKeyInfo
		leaf := &x509.Certificate{SerialNumber: big.NewInt(10), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * time.Minute),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		issued, err = x509.CreateCertificate(rand.Reader, leaf, issuer, csr.PublicKey, issuerKey)
		return issued, err
	}, time.Now)
	dropFirstResponse := true
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dropFirstResponse {
			handler.ServeHTTP(w, r)
			return
		}
		dropFirstResponse = false
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		if response.Code != http.StatusOK {
			t.Errorf("issuance before dropped response returned HTTP %d", response.Code)
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	certificate := leafFixture(t, issuerKey, issuer, false)
	// Issue a server leaf for the listener's IP using the fixture authority.
	original, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	original.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	key := certificate.PrivateKey.(crypto.Signer)
	der, err := x509.CreateCertificate(rand.Reader, original, issuer, key.Public(), issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = guest.EnrollmentTLS(tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key})
	server.StartTLS()
	defer server.Close()
	state := t.TempDir()
	contact := make(chan struct{}, 1)
	poller := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/guest/poll" || len(r.TLS.VerifiedChains) == 0 ||
			!bytes.Equal(r.TLS.VerifiedChains[0][0].Raw, issued) {
			t.Error("poll did not use the enrolled identity")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"protocolVersion":"1","workspaceGeneration":1,"desiredState":"running","serverTime":"2026-11-14T00:00:00Z","operation":null,"certificate":{"renewAfter":"2026-11-14T04:00:00Z"}}`))
		contact <- struct{}{}
	}))
	authority, err := guest.LoadCA(ca, authorityKeyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	poller.TLS = guest.ServerTLS(tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, authority)
	poller.StartTLS()
	defer poller.Close()
	seed := guest.EnrollmentSeed{Grant: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		Endpoint: server.URL + "/internal/v1/enroll", PollEndpoint: poller.URL + "/internal/v1/guest/poll", CAPEM: string(ca)}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.EnrollAgent(t.Context(), state, bytes.NewReader(raw), "1", "test", now); err == nil {
		t.Fatal("first exchange unexpectedly succeeded")
	}
	identity, err := guest.EnrollAgent(t.Context(), state, bytes.NewReader(raw), "1", "test", now)
	if err != nil || !bytes.Equal(identity.Certificate.Certificate[0], issued) {
		t.Fatalf("enrollment retry failed: %v", err)
	}
	loaded, err := guest.LoadAgentIdentity(state, now)
	if err != nil || !bytes.Equal(loaded.Certificate.Certificate[0], issued) {
		t.Fatalf("loading without seed failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- guest.ServeAgent(ctx, state, filepath.Join(state, "absent.json"), "1", "test") }()
	select {
	case <-contact:
		cancel()
		<-done
	case err := <-done:
		t.Fatalf("agent exited without polling the seeded endpoint: %v", err)
	case <-ctx.Done():
		t.Fatal("agent did not poll the seeded endpoint")
	}
	if _, err := guest.LoadAgentIdentity(state, now.Add(time.Hour)); err == nil {
		t.Error("agent accepted an expired identity from disk")
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		path := filepath.Join(state, e.Name())
		info, err := e.Info()
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("state file permissions: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(seed.Grant)) {
			t.Error("enrollment grant persisted outside the seed")
		}
	}
}
