package guest

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrGuestUnauthorized means the authenticated leaf no longer matches current
// registration or workspace authority.
var ErrGuestUnauthorized = errors.New("guest identity is not authorized")

// Contact describes the workspace authority established by an authenticated poll.
type Contact struct {
	WorkspaceID  string
	OwnerID      string
	Repository   string
	Generation   int64
	DesiredState string
	RenewAfter   time.Time
}

// Authenticate checks the leaf against current registration and records
// authenticated contact. The caller obtains the leaf from the verified TLS peer.
func (e *Enrollment) Authenticate(ctx context.Context, leaf *x509.Certificate, now time.Time) (Contact, error) {
	return e.authenticate(ctx, e.db, leaf, now, false)
}

type pollQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (e *Enrollment) authenticate(ctx context.Context, db pollQueryer, leaf *x509.Certificate, now time.Time, allowRetentionShutdown bool) (Contact, error) {
	if e.authority == nil || leaf == nil || leaf.SerialNumber == nil || len(leaf.URIs) != 1 {
		return Contact{}, ErrGuestUnauthorized
	}
	identity := leaf.URIs[0]
	parts := strings.Split(identity.Path, "/")
	if identity.Scheme != "shiftbox" || identity.Host == "" || identity.User != nil ||
		identity.RawQuery != "" || identity.Fragment != "" || len(parts) != 4 || parts[1] != "workspace" || parts[2] == "" {
		return Contact{}, ErrGuestUnauthorized
	}
	generation, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || generation < 1 || parts[3] != strconv.FormatInt(generation, 10) {
		return Contact{}, ErrGuestUnauthorized
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: e.authority.roots(), CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return Contact{}, ErrGuestUnauthorized
	}
	fingerprint := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	stamp := now.UTC().Format("2006-01-02T15:04:05.000000000Z")
	expiry := leaf.NotAfter.UTC().Format(time.RFC3339Nano)
	contact := Contact{WorkspaceID: parts[2], Generation: generation, RenewAfter: leaf.NotBefore.Add(leafReplacement)}
	if leaf.NotAfter.Before(contact.RenewAfter) {
		contact.RenewAfter = leaf.NotBefore
	}
	err = db.QueryRowContext(ctx, `UPDATE guest_registrations SET last_authenticated_at=$1
		WHERE workspace_id=$2 AND generation=$3 AND certificate_serial=$4 AND public_key_fingerprint=$5
		  AND certificate_not_after=$6 AND state='active'
		  AND EXISTS (SELECT 1 FROM workspaces w JOIN installations i ON i.id=w.installation_id
		    JOIN users u ON u.id=w.owner_user_id
		    WHERE w.id=$2 AND w.generation=$3 AND w.installation_id=$7 AND i.state='active'
		      AND u.membership_state='active' AND w.provider_instance_id IS NOT NULL
		      AND (w.desired_state<>'deleted' OR ($8 AND w.deletion_commit_at IS NOT NULL AND EXISTS (
		        SELECT 1 FROM operations o WHERE o.workspace_id=w.id AND o.generation=w.generation
		        AND o.type='delete' AND o.originating_action IN ('workspace.retention_delete','workspace.delete')
		        AND o.status IN ('running','waiting') AND o.current_step='shutdown'))))
		RETURNING (SELECT desired_state FROM workspaces WHERE id=$2),
		          (SELECT owner_user_id FROM workspaces WHERE id=$2),
		          (SELECT repository_url FROM workspaces WHERE id=$2)`,
		stamp, parts[2], generation, leaf.SerialNumber.Text(16), hex.EncodeToString(fingerprint[:]), expiry, identity.Host, allowRetentionShutdown).
		Scan(&contact.DesiredState, &contact.OwnerID, &contact.Repository)
	if errors.Is(err, sql.ErrNoRows) {
		return Contact{}, ErrGuestUnauthorized
	}
	if err != nil {
		return Contact{}, err
	}
	return contact, nil
}
