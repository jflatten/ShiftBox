package guest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"math/big"
	"net/url"
	"strconv"
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
	if now.Before(a.Certificate.NotBefore) || !now.Before(a.Certificate.NotAfter) {
		return nil, nil, ErrEnrollmentDenied
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	serial.SetBit(serial, 128, 1)
	expires := now.Add(leafValidity)
	if a.Certificate.NotAfter.Before(expires) {
		expires = a.Certificate.NotAfter
	}
	expires = expires.Truncate(time.Second)
	if !now.Before(expires) {
		return nil, nil, ErrEnrollmentDenied
	}
	identity := &url.URL{Scheme: "shiftbox", Host: ref.InstallationID, Path: "/workspace/" + ref.WorkspaceID + "/" + strconv.FormatInt(ref.Generation, 10)}
	leaf := &x509.Certificate{
		SerialNumber: serial, NotBefore: now.Truncate(time.Second), NotAfter: expires,
		URIs: []*url.URL{identity}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, a.Certificate, key, a.key)
	return der, leaf, err
}
