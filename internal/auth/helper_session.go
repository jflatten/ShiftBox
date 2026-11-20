package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// ErrHelperCredential means current workstation authorization is unavailable.
var ErrHelperCredential = errors.New("invalid or expired workspace helper credential")

// IssueHelperCredential binds a new scoped verifier to the session that
// approved pairing. Only the caller receives the raw credential.
func (s *Store) IssueHelperCredential(ctx context.Context, tx *sql.Tx, sessionID string, now time.Time) (string, time.Time, error) {
	principal, err := s.PrincipalForSession(ctx, tx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrHelperCredential
	}
	if err != nil {
		return "", time.Time{}, err
	}
	deadline, err := helperSessionDeadline(principal, now)
	if err != nil {
		return "", time.Time{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO helper_credentials(credential_digest,approving_session_id,created_at,expires_at)
		VALUES($1,$2,$3,$4)`, TokenDigest(token), sessionID, authTime(now), authTime(deadline)); err != nil {
		return "", time.Time{}, err
	}
	return token, deadline, nil
}

// HelperPrincipal reads the scoped verifier and rechecks the browser session,
// current membership and applicable installation policy on each request.
func (s *Store) HelperPrincipal(ctx context.Context, tx *sql.Tx, token string, now time.Time) (Principal, time.Time, error) {
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return Principal{}, time.Time{}, ErrHelperCredential
	}
	if _, err := base64.RawURLEncoding.DecodeString(token); err != nil {
		return Principal{}, time.Time{}, ErrHelperCredential
	}
	var sessionID, created, expires string
	err := tx.QueryRowContext(ctx, `SELECT approving_session_id,created_at,expires_at
		FROM helper_credentials WHERE credential_digest=$1`, TokenDigest(token)).Scan(&sessionID, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, time.Time{}, ErrHelperCredential
	}
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	if now.Before(createdAt) || !now.Before(deadline) {
		return Principal{}, time.Time{}, ErrHelperCredential
	}
	principal, err := s.PrincipalForSession(ctx, tx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, time.Time{}, ErrHelperCredential
	}
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	currentDeadline, err := helperSessionDeadline(principal, now)
	if err != nil {
		return Principal{}, time.Time{}, err
	}
	if currentDeadline.Before(deadline) {
		deadline = currentDeadline
	}
	return principal, deadline, nil
}

func helperSessionDeadline(principal Principal, now time.Time) (time.Time, error) {
	workspace := Resource{Type: "workspace", OwnerID: principal.User.ID}
	if principal.Session.Mode != "normal" || !Authorize(principal, "workspace.list", workspace, nil, now).Allowed {
		return time.Time{}, ErrHelperCredential
	}
	deadline := principal.Session.ExpiresAt
	if principal.User.CredentialMode == "external" {
		externalDeadline := principal.Session.ExternalAuthorizedAt.Add(principal.ExternalAuthInterval)
		if externalDeadline.Before(deadline) {
			deadline = externalDeadline
		}
	}
	return deadline, nil
}
