package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

var errBootstrapCertificate = errors.New("local setup certificate is unavailable or invalid; review its protected file")

// EnsureBootstrapCertificate creates a 24-hour loopback identity only when its
// explicit file is absent. The operator must explicitly trust this certificate.
// Existing invalid/expired material is never silently replaced. Call only for an
// unactivated installation; this certificate is never a production fallback.
func EnsureBootstrapCertificate(path string, now time.Time) (tls.Certificate, error) {
	if !filepath.IsAbs(path) || now.IsZero() {
		return tls.Certificate{}, errBootstrapCertificate
	}
	if _, err := os.Lstat(path); err == nil {
		return loadBootstrapCertificate(path, now)
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, errBootstrapCertificate
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	material := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := publishPrivateFile(path, ".bootstrap-tls-*", material); err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	return loadBootstrapCertificate(path, now)
}

// publishPrivateFile atomically creates a durable file without replacing an
// existing identity. The caller reloads the winning file.
func publishPrivateFile(path, pattern string, material []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(material)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func loadBootstrapCertificate(path string, now time.Time) (tls.Certificate, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, errBootstrapCertificate
	}
	file, err := os.Open(path)
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	defer file.Close()
	material, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	pair, err := tls.X509KeyPair(material, material)
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		return tls.Certificate{}, errBootstrapCertificate
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, host := range []string{"127.0.0.1", "::1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return tls.Certificate{}, errBootstrapCertificate
		}
	}
	pair.Leaf = leaf
	return pair, nil
}
