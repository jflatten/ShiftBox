package guest

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/secret"
)

// SecretResolvePrefix is the workspace secret endpoint on the mTLS listener.
const SecretResolvePrefix = "/internal/v1/guest/secrets/"

// SecretListPath lists effective secret names on the authenticated guest listener.
const SecretListPath = "/internal/v1/guest/secrets"

// ResolveSecret authenticates the leaf and selects the secret within one
// transaction. The request supplies only the name.
func (e *Enrollment) ResolveSecret(ctx context.Context, leaf *x509.Certificate, now time.Time,
	service *secret.Service, name string,
) ([]byte, string, error) {
	var value []byte
	var scope string
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now, false)
		if err != nil {
			return err
		}
		value, scope, err = service.ResolveWorkspace(ctx, tx, contact.WorkspaceID, contact.Generation, name)
		if errors.Is(err, secret.ErrWorkspaceDenied) {
			return ErrGuestUnauthorized
		}
		return err
	})
	if err != nil {
		clear(value)
		return nil, "", err
	}
	return value, scope, nil
}

// ListSecrets authenticates the current guest and derives available key names
// in the same transaction, without retrieving any protected secret values.
func (e *Enrollment) ListSecrets(ctx context.Context, leaf *x509.Certificate, now time.Time,
	service *secret.Service,
) ([]secret.Key, error) {
	var keys []secret.Key
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now, false)
		if err != nil {
			return err
		}
		keys, err = service.ListWorkspace(ctx, tx, contact.WorkspaceID, contact.Generation)
		if errors.Is(err, secret.ErrWorkspaceDenied) {
			return ErrGuestUnauthorized
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
