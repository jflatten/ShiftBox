package guest_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
)

func authorityFixture(t *testing.T) ([]byte, []byte, *ecdsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ShiftBox test installation"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), key, cert
}

func leafFixture(t *testing.T, key *ecdsa.PrivateKey, ca *x509.Certificate, client bool) tls.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	usage := x509.ExtKeyUsageServerAuth
	if client {
		usage = x509.ExtKeyUsageClientAuth
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"controller.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: leafKey}
}

// TestCALoadPreservesIdentity prevents controller restarts from replacing guest trust.
func TestCALoadPreservesIdentity(t *testing.T) {
	cert, key, _, original := authorityFixture(t)
	for range 2 {
		ca, err := guest.LoadCA(cert, key, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ca.Certificate.Raw, original.Raw) {
			t.Error("LoadCA changed installation identity")
		}
	}
	_, otherKey, _, _ := authorityFixture(t)
	if _, err := guest.LoadCA(cert, otherKey, time.Now()); err == nil {
		t.Error("LoadCA accepted mismatched key")
	}
	if _, err := guest.LoadCA(cert, key, time.Now().Add(2*time.Hour)); err == nil {
		t.Error("LoadCA accepted expired authority")
	}
	if _, err := guest.LoadCA([]byte("invalid"), key, time.Now()); err == nil {
		t.Error("LoadCA accepted invalid certificate")
	}
}

// TestMutualTLSRejectsUnknownGuest verifies the actual transport denies unregistered CA roots.
func TestMutualTLSRejectsUnknownGuest(t *testing.T) {
	certPEM, keyPEM, key, cert := authorityFixture(t)
	ca, err := guest.LoadCA(certPEM, keyPEM, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.TLS = guest.ServerTLS(leafFixture(t, key, cert, false), ca)
	server.StartTLS()
	defer server.Close()
	clientConfig := guest.ClientTLS(leafFixture(t, key, cert, true), ca, "controller.test")
	transport := &http.Transport{TLSClientConfig: clientConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.TLS.Version != tls.VersionTLS13 {
		t.Errorf("TLS version = %x, want TLS 1.3", response.TLS.Version)
	}
	_, _, otherKey, otherCert := authorityFixture(t)
	badTransport := &http.Transport{TLSClientConfig: guest.ClientTLS(leafFixture(t, otherKey, otherCert, true), ca, "controller.test")}
	defer badTransport.CloseIdleConnections()
	bad := &http.Client{Transport: badTransport, Timeout: 2 * time.Second}
	if response, err := bad.Get(server.URL); err == nil {
		response.Body.Close()
		t.Error("server accepted guest signed by another installation")
	}
}
