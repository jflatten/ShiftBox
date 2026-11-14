package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

// CredentialImporter creates a credential in the separately configured Dex.
type CredentialImporter interface {
	Ensure(context.Context, string, string, []byte) error
}

type bootstrapCredential struct {
	email, userID string
	hash          []byte
}

// TransferBootstrapCredential copies the single changed bootstrap credential to
// a separately configured Dex through its authenticated password API.
func TransferBootstrapCredential(ctx context.Context, path, email, userID string, target CredentialImporter) error {
	if target == nil {
		return ErrPasswordAdministration
	}
	record, err := readBootstrapCredential(ctx, path, email, userID)
	if err != nil {
		return err
	}
	return target.Ensure(ctx, record.email, record.userID, record.hash)
}

// readBootstrapCredential opens the pinned Dex SQLite schema in read-only mode.
// Call after the controller has recorded completion of the first password change.
// Dex's ListPasswords RPC omits the hash, so the transfer reads only the one
// bootstrap password record and rejects an already populated identity store.
func readBootstrapCredential(ctx context.Context, path, email, userID string) (bootstrapCredential, error) {
	if !filepath.IsAbs(path) || email == "" || userID == "" {
		return bootstrapCredential{}, ErrPasswordAdministration
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return bootstrapCredential{}, ErrPasswordAdministration
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return bootstrapCredential{}, ErrPasswordAdministration
	}
	defer db.Close()
	var record bootstrapCredential
	err = db.QueryRowContext(ctx, `SELECT email, user_id, hash FROM password
		WHERE email=$1 AND user_id=$2 AND (SELECT COUNT(*) FROM password)=1`,
		email, userID).Scan(&record.email, &record.userID, &record.hash)
	if errors.Is(err, sql.ErrNoRows) {
		return bootstrapCredential{}, ErrLocalIdentityConflict
	}
	if err != nil {
		return bootstrapCredential{}, ErrPasswordAdministration
	}
	if _, err := bcrypt.Cost(record.hash); err != nil {
		return bootstrapCredential{}, ErrPasswordAdministration
	}
	return record, nil
}
