package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"time"

	"github.com/google/uuid"
)

const (
	entryGrantLifetime    = time.Minute
	serviceCookieLifetime = time.Hour
)

// ErrOriginGrant identifies an invalid, expired, revoked, or consumed credential.
var ErrOriginGrant = errors.New("workspace origin credential is not valid")

// OriginScope identifies one workspace service over its exact browser origin.
type OriginScope struct {
	WorkspaceID string
	Generation  int64
	Service     string
	Origin      string
}

// OriginAuthority records the session and exact authorization used at issuance.
type OriginAuthority struct {
	ID                          string
	Scope                       OriginScope
	UserID, SessionID           string
	Permission, ResourceGrantID string
}

// OriginExchange carries a fresh service credential and its durable authority.
type OriginExchange struct {
	Cookie    string
	ExpiresAt time.Time
	Authority OriginAuthority
}

// IssueOriginGrant persists one exchange credential under the caller's current
// session and resource authorization transaction.
func IssueOriginGrant(ctx context.Context, tx *sql.Tx, authority OriginAuthority, now time.Time) (string, time.Time, error) {
	scope := authority.Scope
	if !scope.valid() || authority.UserID == "" || authority.SessionID == "" ||
		!(authority.Permission == "workspace.connect_owner" && authority.ResourceGrantID == "" ||
			authority.Permission == "workspace.connect_admin" && authority.ResourceGrantID != "") {
		return "", time.Time{}, ErrOriginGrant
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", time.Time{}, err
	}
	token, digest, err := newOriginCredential()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.UTC().Add(entryGrantLifetime)
	_, err = tx.ExecContext(ctx, `INSERT INTO origin_grants(id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,
		permission_basis,resource_grant_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id.String(), digest, authority.UserID, authority.SessionID, scope.WorkspaceID, scope.Generation,
		scope.Service, scope.Origin, authority.Permission, authority.ResourceGrantID, grantTime(expires))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// ConsumeOriginGrant exchanges a token once. The caller must validate the
// returned authority and roll back the transaction if it has changed.
func ConsumeOriginGrant(ctx context.Context, tx *sql.Tx, scope OriginScope, token string, now time.Time) (OriginExchange, error) {
	digest, ok := originDigest(token)
	if !ok || !scope.valid() {
		return OriginExchange{}, ErrOriginGrant
	}
	cookie, cookieDigest, err := newOriginCredential()
	if err != nil {
		return OriginExchange{}, err
	}
	expires := now.UTC().Add(serviceCookieLifetime)
	authority := OriginAuthority{Scope: scope}
	err = tx.QueryRowContext(ctx, `UPDATE origin_grants SET consumed_at=$1,cookie_digest=$2,cookie_expires_at=$3
		WHERE token_digest=$4 AND workspace_id=$5 AND generation=$6 AND service_key=$7 AND origin=$8
		  AND expires_at>$1 AND consumed_at IS NULL AND revoked_at IS NULL
		RETURNING id,user_id,session_id,permission_basis,resource_grant_id`,
		grantTime(now), cookieDigest, grantTime(expires), digest, scope.WorkspaceID, scope.Generation, scope.Service, scope.Origin).
		Scan(&authority.ID, &authority.UserID, &authority.SessionID, &authority.Permission, &authority.ResourceGrantID)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginExchange{}, ErrOriginGrant
	}
	if err != nil {
		return OriginExchange{}, err
	}
	return OriginExchange{Cookie: cookie, ExpiresAt: expires, Authority: authority}, nil
}

// ReadOriginCookie recovers a service credential only within its original
// scope. Callers must also recheck current workspace and session authority.
func ReadOriginCookie(ctx context.Context, tx *sql.Tx, scope OriginScope, cookie string, now time.Time) (OriginAuthority, error) {
	digest, ok := originDigest(cookie)
	if !ok || !scope.valid() {
		return OriginAuthority{}, ErrOriginGrant
	}
	authority := OriginAuthority{Scope: scope}
	err := tx.QueryRowContext(ctx, `SELECT id,user_id,session_id,permission_basis,resource_grant_id FROM origin_grants
		WHERE cookie_digest=$1 AND workspace_id=$2 AND generation=$3 AND service_key=$4 AND origin=$5
		  AND cookie_expires_at>$6 AND consumed_at IS NOT NULL AND revoked_at IS NULL`,
		digest, scope.WorkspaceID, scope.Generation, scope.Service, scope.Origin, grantTime(now)).
		Scan(&authority.ID, &authority.UserID, &authority.SessionID, &authority.Permission, &authority.ResourceGrantID)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginAuthority{}, ErrOriginGrant
	}
	if err != nil {
		return OriginAuthority{}, err
	}
	return authority, nil
}

func (s OriginScope) valid() bool {
	u, err := url.Parse(s.Origin)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.EscapedPath() == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && s.Origin == u.String() &&
		s.WorkspaceID != "" && s.Generation > 0 && len(s.Service) <= 63 && tunnelServiceKey.MatchString(s.Service)
}

func newOriginCredential() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	digest, _ := originDigest(token)
	return token, digest, nil
}

func originDigest(token string) (string, bool) {
	if len(token) != 43 {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", false
	}
	hash := sha256.Sum256(decoded)
	return hex.EncodeToString(hash[:]), true
}

func grantTime(stamp time.Time) string {
	return stamp.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
