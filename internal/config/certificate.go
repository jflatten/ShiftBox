package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"time"
)

// ErrInvalidBrowserCertificate is safe to translate into public replacement guidance.
var ErrInvalidBrowserCertificate = errors.New("browser certificate does not satisfy the configured HTTPS requirements")

// ValidateBrowserCertificate verifies a matching key, server-auth chain, validity,
// and every concrete management/IDE/preview hostname against explicit trust roots.
// The consuming activation flow supplies its trust policy and derives hostnames
// from configured origin patterns. Validation never installs or persists a key.
func ValidateBrowserCertificate(chainPEM, keyPEM []byte, hostnames []string, roots *x509.CertPool, now time.Time) (tls.Certificate, error) {
	if len(hostnames) == 0 || roots == nil || now.IsZero() {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	pair, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return tls.Certificate{}, ErrInvalidBrowserCertificate
		}
		intermediates.AddCert(certificate)
	}
	for _, hostname := range hostnames {
		if hostname == "" {
			return tls.Certificate{}, ErrInvalidBrowserCertificate
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, DNSName: hostname, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return tls.Certificate{}, ErrInvalidBrowserCertificate
		}
	}
	pair.Leaf = leaf
	return pair, nil
}

// ValidateCertificate verifies the configured management hostname and requires
// wildcard SANs for both service namespaces, including workspaces created later.
// Trust roots remain an explicit activation input; uploaded keys are not stored.
func (h *ServiceHosts) ValidateCertificate(chainPEM, keyPEM []byte, roots *x509.CertPool, now time.Time) (tls.Certificate, error) {
	if h == nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	wildcards := []string{"*" + h.ideSuffix, "*" + h.previewSuffix}
	pair, err := ValidateBrowserCertificate(chainPEM, keyPEM, append([]string{h.management}, wildcards...), roots, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	for _, wildcard := range wildcards {
		covered := false
		for _, name := range pair.Leaf.DNSNames {
			if strings.EqualFold(name, wildcard) {
				covered = true
				break
			}
		}
		if !covered {
			return tls.Certificate{}, ErrInvalidBrowserCertificate
		}
	}
	return pair, nil
}
