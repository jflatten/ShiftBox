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

// ResolveSecret authenticates the leaf and selects the secret within one
// transaction. The request supplies only the name.
func (e *Enrollment) ResolveSecret(ctx context.Context, leaf *x509.Certificate, now time.Time,
	service *secret.Service, name string,
) ([]byte, string, error) {
	var value []byte
	var scope string
	err := database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now)
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
