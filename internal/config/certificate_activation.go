package config

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"time"
)

// BrowserCertificateInput holds an upload only for the duration of activation.
// Private key bytes must never enter configuration snapshots, logs, or responses.
type BrowserCertificateInput struct {
	ID       string
	ChainPEM []byte
	KeyPEM   []byte
}

// ActivateBrowserCertificate validates and encrypts a replacement and records it
// with the new configuration generation in the caller's transaction. Any error
// requires rollback. Listener changes occur only after the transaction commits.
func ActivateBrowserCertificate(ctx context.Context, tx *sql.Tx, activation Activation, settings InstallationSettings, input BrowserCertificateInput, cipher *cryptostore.Store, roots *x509.CertPool) (int64, error) {
	if cipher == nil || !workspaceLabel.MatchString(input.ID) {
		return 0, errors.New("invalid browser certificate storage inputs")
	}
	hosts, err := NewServiceHosts(settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern)
	if err != nil {
		return 0, err
	}
	pair, err := hosts.ValidateCertificate(input.ChainPEM, input.KeyPEM, roots, activation.ActivatedAt)
	if err != nil {
		return 0, err
	}
	value, err := cipher.Seal(browserKeyContext(activation.InstallationID, input.ID), input.KeyPEM)
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	settings.BrowserCertificateID = input.ID
	generation, err := RecordInstallationSettings(ctx, tx, activation, settings)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO protected_values(id,value_json) VALUES($1,$2)`, input.ID, string(encoded)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE browser_certificates SET state='replaced' WHERE state='active'`); err != nil {
		return 0, err
	}
	fingerprint := sha256.Sum256(pair.Leaf.Raw)
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_certificates(id,chain_pem,private_key_ciphertext_id,not_before,not_after,fingerprint_sha256,state) VALUES($1,$2,$1,$3,$4,$5,'active')`, input.ID, string(input.ChainPEM), pair.Leaf.NotBefore.UTC().Format(time.RFC3339Nano), pair.Leaf.NotAfter.UTC().Format(time.RFC3339Nano), hex.EncodeToString(fingerprint[:]))
	if err != nil {
		return 0, err
	}
	return generation, nil
}

// LoadBrowserCertificate decrypts only the active record and revalidates current
// validity, trust, and configured hostname coverage. An expired replacement never
// causes a fallback to an older certificate or a temporary production identity.
func LoadBrowserCertificate(ctx context.Context, db *sql.DB, installationID, id string, cipher *cryptostore.Store, roots *x509.CertPool, now time.Time) (tls.Certificate, error) {
	if cipher == nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	var chain, encoded, management, ide, preview string
	err := db.QueryRowContext(ctx, `SELECT c.chain_pem,p.value_json,i.management_origin,i.ide_host_pattern,i.preview_host_pattern FROM browser_certificates c JOIN protected_values p ON p.id=c.private_key_ciphertext_id JOIN installations i ON i.id=$1 WHERE c.id=$2 AND c.state='active'`, installationID, id).Scan(&chain, &encoded, &management, &ide, &preview)
	if err != nil {
		return tls.Certificate{}, err
	}
	var value cryptostore.Value
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	key, err := cipher.Open(browserKeyContext(installationID, id), value)
	if err != nil {
		return tls.Certificate{}, ErrInvalidBrowserCertificate
	}
	hosts, err := NewServiceHosts(management, ide, preview)
	if err != nil {
		return tls.Certificate{}, err
	}
	return hosts.ValidateCertificate([]byte(chain), key, roots, now)
}

func browserKeyContext(installationID, id string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "browser_certificate", Scope: "installation", OwnerID: installationID, RecordID: id, Name: "private_key"}
}
