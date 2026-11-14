package guest

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// RenewalPath is available only on the authenticated guest listener.
const RenewalPath = "/internal/v1/guest/certificates:renew"

// Renew replaces the current client leaf and keeps one issuance receipt so a
// lost response can be retried with the same fresh key while the old TLS leaf
// remains valid. Ordinary guest requests immediately require the new leaf.
func (e *Enrollment) Renew(ctx context.Context, leaf *x509.Certificate, csr []byte, now time.Time) ([]byte, error) {
	ref, err := e.verifiedGuestRef(leaf, now)
	if err != nil || now.Before(guestRenewAfter(leaf)) {
		return nil, ErrGuestUnauthorized
	}
	key, proof, err := guestKeyProof(csr)
	if err != nil {
		return nil, ErrGuestUnauthorized
	}
	oldFingerprint := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	oldProof := hex.EncodeToString(oldFingerprint[:])
	if proof == oldProof {
		return nil, ErrGuestUnauthorized
	}
	oldSerial := leaf.SerialNumber.Text(16)
	var certificate []byte
	err = database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		if err := lockRegistration(ctx, tx, ref.WorkspaceID, ref.Generation); err != nil {
			if errors.Is(err, ErrEnrollmentDenied) {
				return ErrGuestUnauthorized
			}
			return err
		}
		var serial, fingerprint, expiry string
		var predecessor, receipt sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT r.certificate_serial,r.public_key_fingerprint,r.certificate_not_after,
			r.renewal_predecessor_serial,r.renewal_certificate_der
			FROM guest_registrations r JOIN workspaces w ON w.id=r.workspace_id AND w.generation=r.generation
			JOIN installations i ON i.id=w.installation_id JOIN users u ON u.id=w.owner_user_id
			WHERE w.id=$1 AND w.generation=$2 AND i.id=$3 AND i.state='active' AND r.state='active'
			AND u.membership_state='active' AND w.provider_instance_id IS NOT NULL AND w.desired_state='running'`,
			ref.WorkspaceID, ref.Generation, ref.InstallationID).Scan(&serial, &fingerprint, &expiry, &predecessor, &receipt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGuestUnauthorized
		}
		if err != nil {
			return err
		}
		if predecessor.Valid && predecessor.String == oldSerial && receipt.Valid && fingerprint == proof {
			certificate, err = hex.DecodeString(receipt.String)
			return err
		}
		if serial != oldSerial || fingerprint != oldProof || expiry != leaf.NotAfter.UTC().Format(time.RFC3339Nano) {
			return ErrGuestUnauthorized
		}
		der, replacement, err := e.authority.issueGuest(ref, key, now)
		if err != nil {
			return err
		}
		if !replacement.NotAfter.After(leaf.NotAfter) {
			return ErrGuestUnauthorized
		}
		_, err = tx.ExecContext(ctx, `UPDATE guest_registrations SET certificate_serial=$3,public_key_fingerprint=$4,
			certificate_not_after=$5,renewal_predecessor_serial=$6,renewal_certificate_der=$7
			WHERE workspace_id=$1 AND generation=$2`, ref.WorkspaceID, ref.Generation, replacement.SerialNumber.Text(16),
			proof, replacement.NotAfter.UTC().Format(time.RFC3339Nano), oldSerial, hex.EncodeToString(der))
		certificate = der
		return err
	})
	if err != nil {
		return nil, err
	}
	return certificate, nil
}
