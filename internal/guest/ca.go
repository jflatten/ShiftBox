package guest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// Authority is the retained installation identity used only inside the controller.
// Its signing key is never part of a guest response or exported read model.
type Authority struct {
	Certificate *x509.Certificate
	key         *ecdsa.PrivateKey
}

// LoadCA validates existing ECDSA P-256 authority material without regenerating it.
func LoadCA(certificatePEM, keyPEM []byte, now time.Time) (*Authority, error) {
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil {
		return nil, errors.New("invalid installation authority key pair")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid installation authority certificate")
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("installation authority requires ECDSA P-256")
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, errors.New("installation authority is not currently valid for signing")
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return nil, errors.New("installation authority must be self-signed")
	}
	return &Authority{Certificate: cert, key: key}, nil
}

func (a *Authority) roots() *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(a.Certificate)
	return roots
}
