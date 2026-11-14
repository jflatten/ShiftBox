package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/guest"
)

const installationCAFile = "guest-ca.json"

var errInstallationCA = errors.New("installation guest CA is unavailable or invalid")

type installationCARecord struct {
	Certificate string            `json:"certificatePem"`
	Key         cryptostore.Value `json:"key"`
}

// EnsureInstallationCA provisions an authority for an unactivated installation.
// An existing CA must load and verify before setup can continue.
func EnsureInstallationCA(directory, id string, cipher *cryptostore.Store, now time.Time) (*guest.Authority, error) {
	authority, err := LoadInstallationCA(directory, id, cipher, now)
	if !errors.Is(err, os.ErrNotExist) {
		return authority, err
	}
	record, err := createInstallationCA(id, cipher, now)
	if err != nil {
		return nil, errInstallationCA
	}
	path := filepath.Join(directory, installationCAFile)
	if err := publishPrivateFile(path, ".guest-ca-*", record); err != nil {
		return nil, errInstallationCA
	}
	return LoadInstallationCA(directory, id, cipher, now)
}

// LoadInstallationCA opens the retained guest authority without creating or
// replacing trust material. The signing key is scoped to the installation.
func LoadInstallationCA(directory, id string, cipher *cryptostore.Store, now time.Time) (*guest.Authority, error) {
	if !filepath.IsAbs(directory) || id == "" || cipher == nil || now.IsZero() {
		return nil, errInstallationCA
	}
	path := filepath.Join(directory, installationCAFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(errInstallationCA, os.ErrNotExist)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errInstallationCA
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errInstallationCA
	}
	defer file.Close()
	const limit = 64 << 10
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	var record installationCARecord
	if err != nil || len(content) > limit || json.Unmarshal(content, &record) != nil {
		return nil, errInstallationCA
	}
	key, err := cipher.Open(installationCAContext(id), record.Key)
	if err != nil {
		return nil, errInstallationCA
	}
	defer clear(key)
	authority, err := guest.LoadCA([]byte(record.Certificate), key, now)
	if err != nil {
		return nil, errInstallationCA
	}
	return authority, nil
}

func createInstallationCA(id string, cipher *cryptostore.Store, now time.Time) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	serial.SetBit(serial, 128, 1)
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "ShiftBox Guest CA"},
		NotBefore: now.Truncate(time.Second), NotAfter: now.AddDate(5, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	defer clear(keyDER)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	defer clear(keyPEM)
	sealed, err := cipher.Seal(installationCAContext(id), keyPEM)
	if err != nil {
		return nil, err
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return json.Marshal(installationCARecord{Certificate: string(certificate), Key: sealed})
}

func installationCAContext(id string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "guest_ca", Scope: "installation",
		OwnerID: id, RecordID: "root", Name: "signing_key"}
}
