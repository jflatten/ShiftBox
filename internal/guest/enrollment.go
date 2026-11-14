package guest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

const grantLifetime = 15 * time.Minute

// ErrEnrollmentDenied identifies requests that cannot establish guest identity.
var ErrEnrollmentDenied = errors.New("guest enrollment denied")

// Enrollment issues and consumes provider-bound guest grants.
type Enrollment struct {
	db        *sql.DB
	authority *Authority
}

// NewEnrollment binds enrollment to the installation database and CA.
func NewEnrollment(db *sql.DB, authority *Authority) *Enrollment {
	return &Enrollment{db: db, authority: authority}
}

// Mint requires a fresh stopped observation for the exact workspace and
// current authorized operation. Only the returned token goes to delivery media.
func (e *Enrollment) Mint(ctx context.Context, operationID string, inventory provider.Inventory, now time.Time) (string, error) {
	ref := inventory.Ref
	if inventory.Power != provider.Stopped || ref.InstanceID == "" || inventory.InstanceID != ref.InstanceID ||
		inventory.ObservedAt.IsZero() || inventory.ObservedAt.After(now) || now.Sub(inventory.ObservedAt) > time.Minute {
		return "", ErrEnrollmentDenied
	}
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(value[:])
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		if err := lockRegistration(ctx, tx, ref.WorkspaceID, ref.Generation); err != nil {
			return err
		}
		current, state, _, _, err := enrollmentTarget(ctx, tx, ref.WorkspaceID, ref.Generation, operationID)
		if err != nil {
			return err
		}
		if current != ref || (state != "eligible" && state != "expired") {
			return ErrEnrollmentDenied
		}
		if _, err := tx.ExecContext(ctx, `UPDATE enrollment_grants SET state='invalidated'
			WHERE workspace_id=$1 AND generation=$2 AND state IN ('issued','consumed')`, ref.WorkspaceID, ref.Generation); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO enrollment_grants
			(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state)
			VALUES($1,$2,$3,$4,$5,$6,'issued')`, auth.TokenDigest(token), ref.WorkspaceID, ref.Generation, operationID, ref.InstanceID, now.Add(grantLifetime).UTC().Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Enroll consumes a grant atomically with issuance. A retry may recover the
// exact certificate while the grant and recorded registration remain valid.
func (e *Enrollment) Enroll(ctx context.Context, token string, csr []byte, now time.Time) ([]byte, error) {
	if len(token) != 43 || e.authority == nil {
		return nil, ErrEnrollmentDenied
	}
	key, proof, err := guestKeyProof(csr)
	if err != nil {
		return nil, err
	}
	id := auth.TokenDigest(token)
	var certificate []byte
	err = database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		var workspace string
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT workspace_id,generation FROM enrollment_grants WHERE id=$1`, id).Scan(&workspace, &generation); err != nil {
			return denyMissing(err)
		}
		if err := lockRegistration(ctx, tx, workspace, generation); err != nil {
			return err
		}
		var operationID, instance, expires, state, usedProof, issued string
		err := tx.QueryRowContext(ctx, `SELECT operation_id,provider_instance_id,expires_at,state,COALESCE(key_proof,''),COALESCE(certificate_der,'')
			FROM enrollment_grants WHERE id=$1`, id).Scan(&operationID, &instance, &expires, &state, &usedProof, &issued)
		if err != nil {
			return denyMissing(err)
		}
		deadline, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return err
		}
		if !now.Before(deadline) || (state != "issued" && state != "consumed") {
			return ErrEnrollmentDenied
		}
		ref, registration, currentProof, validUntil, err := enrollmentTarget(ctx, tx, workspace, generation, operationID)
		if err != nil {
			return err
		}
		if ref.InstanceID != instance {
			return ErrEnrollmentDenied
		}
		if state == "consumed" {
			leafExpiry, err := time.Parse(time.RFC3339Nano, validUntil)
			if registration != "active" || usedProof != proof || currentProof != proof || err != nil || !now.Before(leafExpiry) {
				return ErrEnrollmentDenied
			}
			certificate, err = hex.DecodeString(issued)
			return err
		}
		if registration != "eligible" && registration != "expired" {
			return ErrEnrollmentDenied
		}
		der, leaf, err := e.authority.issueGuest(ref, key, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE guest_registrations SET state='active',public_key_fingerprint=$3,
			certificate_serial=$4,certificate_not_after=$5 WHERE workspace_id=$1 AND generation=$2`,
			workspace, generation, proof, leaf.SerialNumber.Text(16), leaf.NotAfter.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE enrollment_grants SET state='consumed',key_proof=$2,certificate_der=$3 WHERE id=$1`, id, proof, hex.EncodeToString(der)); err != nil {
			return err
		}
		certificate = der
		return nil
	})
	if err != nil {
		return nil, err
	}
	return certificate, nil
}

func lockRegistration(ctx context.Context, tx *sql.Tx, workspace string, generation int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE guest_registrations SET state=state WHERE workspace_id=$1 AND generation=$2`, workspace, generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrEnrollmentDenied
	}
	return nil
}

func enrollmentTarget(ctx context.Context, tx *sql.Tx, workspace string, generation int64, op string) (provider.WorkspaceRef, string, string, string, error) {
	ref := provider.WorkspaceRef{WorkspaceID: workspace, Generation: generation}
	var state, proof, expires string
	err := tx.QueryRowContext(ctx, `SELECT w.installation_id,w.ownership_marker,w.provider_instance_id,r.state,
		COALESCE(r.public_key_fingerprint,''),COALESCE(r.certificate_not_after,'')
		FROM workspaces w JOIN guest_registrations r ON r.workspace_id=w.id AND r.generation=w.generation
		JOIN operations o ON o.workspace_id=w.id AND o.generation=w.generation
		JOIN users u ON u.id=w.owner_user_id JOIN installations i ON i.id=w.installation_id
		WHERE w.id=$1 AND w.generation=$2 AND o.id=$3 AND w.desired_state='running' AND i.state='active'
		  AND w.provider_instance_id IS NOT NULL AND u.membership_state='active'
		  AND o.type IN ('provision','start','recover') AND o.status IN ('running','waiting')`, workspace, generation, op).
		Scan(&ref.InstallationID, &ref.OwnershipMarker, &ref.InstanceID, &state, &proof, &expires)
	return ref, state, proof, expires, denyMissing(err)
}

func denyMissing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnrollmentDenied
	}
	return err
}
