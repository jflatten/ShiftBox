package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
)

// TestBrowserCertificateCoversEveryService prevents activating HTTPS when only management, but not IDE/preview, is covered.
func TestBrowserCertificateCoversEveryService(t *testing.T) {
	chain, key, roots, now := browserCertificateFixture(t, x509.ExtKeyUsageServerAuth)
	hosts := []string{"management.test", "workspace.ide.test", "workspace-service.preview.test"}
	pair, err := config.ValidateBrowserCertificate(chain, key, hosts, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf == nil || pair.PrivateKey == nil {
		t.Fatal("ValidateBrowserCertificate(valid) returned incomplete TLS material")
	}
	for _, hosts := range [][]string{nil, {"management.test", "uncovered.test"}, {"nested.workspace.ide.test"}} {
		if _, err := config.ValidateBrowserCertificate(chain, key, hosts, roots, now); !errors.Is(err, config.ErrInvalidBrowserCertificate) {
			t.Errorf("ValidateBrowserCertificate(hosts %v) = %v, want certificate rejection", hosts, err)
		}
	}
}

// TestBrowserCertificateRejectsBrokenTrust rejects a wrong key, expired/not-yet-valid certificate, unknown issuer, or malformed PEM.
func TestBrowserCertificateRejectsBrokenTrust(t *testing.T) {
	chain, key, roots, now := browserCertificateFixture(t, x509.ExtKeyUsageServerAuth)
	_, otherKey, _, _ := browserCertificateFixture(t, x509.ExtKeyUsageServerAuth)
	for _, tc := range []struct {
		chain, key []byte
		roots      *x509.CertPool
		now        time.Time
	}{
		{chain, otherKey, roots, now}, {chain, key, roots, now.Add(2 * time.Hour)},
		{chain, key, roots, now.Add(-2 * time.Hour)}, {chain, key, x509.NewCertPool(), now},
		{[]byte("private malformed certificate"), key, roots, now},
	} {
		pair, err := config.ValidateBrowserCertificate(tc.chain, tc.key, []string{"management.test"}, tc.roots, tc.now)
		if !errors.Is(err, config.ErrInvalidBrowserCertificate) || pair.PrivateKey != nil {
			t.Errorf("ValidateBrowserCertificate(invalid trust) = %v with private key=%t, want safe rejection", err, pair.PrivateKey != nil)
		}
	}
}

// TestBrowserCertificateRejectsClientOnlyUsage prevents a trusted client certificate from becoming the browser server identity.
func TestBrowserCertificateRejectsClientOnlyUsage(t *testing.T) {
	chain, key, roots, now := browserCertificateFixture(t, x509.ExtKeyUsageClientAuth)
	if _, err := config.ValidateBrowserCertificate(chain, key, []string{"management.test"}, roots, now); !errors.Is(err, config.ErrInvalidBrowserCertificate) {
		t.Errorf("ValidateBrowserCertificate(client-only) = %v, want rejection", err)
	}
}

func browserCertificateFixture(t *testing.T, usage x509.ExtKeyUsage) ([]byte, []byte, *x509.CertPool, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), DNSNames: []string{"management.test", "*.ide.test", "*.preview.test"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
	return chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), roots, now
}
