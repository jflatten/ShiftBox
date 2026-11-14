package guest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

const (
	leafValidity    = 8 * time.Hour
	leafReplacement = 4 * time.Hour
)

func guestKeyProof(der []byte) (*ecdsa.PublicKey, string, error) {
	if len(der) == 0 || len(der) > 8192 {
		return nil, "", ErrEnrollmentDenied
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		return nil, "", ErrEnrollmentDenied
	}
	key, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, "", ErrEnrollmentDenied
	}
	sum := sha256.Sum256(csr.RawSubjectPublicKeyInfo)
	return key, hex.EncodeToString(sum[:]), nil
}

func (a *Authority) issueGuest(ref provider.WorkspaceRef, key *ecdsa.PublicKey, now time.Time) ([]byte, *x509.Certificate, error) {
	leaf, err := a.leafTemplate(now, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return nil, nil, err
	}
	identity := &url.URL{Scheme: "shiftbox", Host: ref.InstallationID, Path: "/workspace/" + ref.WorkspaceID + "/" + strconv.FormatInt(ref.Generation, 10)}
	leaf.URIs = []*url.URL{identity}
	der, err := x509.CreateCertificate(rand.Reader, leaf, a.Certificate, key, a.key)
	return der, leaf, err
}

// ServerLeaf issues a fresh key and TLS server leaf for the configured internal
// guest endpoint names.
func (a *Authority) ServerLeaf(names []string, now time.Time) (tls.Certificate, error) {
	leaf, err := a.leafTemplate(now, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(names) == 0 {
		return tls.Certificate{}, errors.New("internal TLS requires a server name")
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			leaf.IPAddresses = append(leaf.IPAddresses, ip)
			continue
		}
		if name == "" || strings.ContainsAny(name, "*:/ ") {
			return tls.Certificate{}, errors.New("invalid internal TLS name")
		}
		leaf.DNSNames = append(leaf.DNSNames, name)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, a.Certificate, &key.PublicKey, a.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	for _, name := range names {
		if err := parsed.VerifyHostname(name); err != nil {
			return tls.Certificate{}, errors.New("invalid internal TLS name")
		}
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, nil
}

func (a *Authority) leafTemplate(now time.Time, usage x509.ExtKeyUsage) (*x509.Certificate, error) {
	if now.Before(a.Certificate.NotBefore) || !now.Before(a.Certificate.NotAfter) {
		return nil, ErrEnrollmentDenied
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	serial.SetBit(serial, 128, 1)
	expires := now.Add(leafValidity)
	if a.Certificate.NotAfter.Before(expires) {
		expires = a.Certificate.NotAfter
	}
	expires = expires.Truncate(time.Second)
	if !now.Before(expires) {
		return nil, ErrEnrollmentDenied
	}
	return &x509.Certificate{
		SerialNumber: serial, NotBefore: now.Truncate(time.Second), NotAfter: expires,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
	}, nil
}
