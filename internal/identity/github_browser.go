package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
)

// ErrGitHubState identifies a missing, expired, or already used browser flow.
var ErrGitHubState = errors.New("GitHub authorization request is no longer valid")

// GitHubAuthorizationStart describes the browser destination and expiry.
type GitHubAuthorizationStart struct {
	URL       string    `json:"authorizationUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// BeginAuthorization creates a short-lived state for the authenticated owner.
func (g *GitHub) BeginAuthorization(ctx context.Context, owner, origin string) (GitHubAuthorizationStart, error) {
	return g.beginAuthorization(ctx, g.service.store.db, owner, origin)
}

// BeginAuthorizationInTx saves OAuth intent alongside the caller's session check.
func (g *GitHub) BeginAuthorizationInTx(ctx context.Context, tx *sql.Tx, owner, origin string) (GitHubAuthorizationStart, error) {
	return g.beginAuthorization(ctx, tx, owner, origin)
}

func (g *GitHub) beginAuthorization(ctx context.Context, db githubWriter, owner, origin string) (GitHubAuthorizationStart, error) {
	redirect, err := githubCallback(origin)
	if err != nil {
		return GitHubAuthorizationStart{}, err
	}
	var random [64]byte
	if _, err := rand.Read(random[:]); err != nil {
		return GitHubAuthorizationStart{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(random[:32])
	verifier := base64.RawURLEncoding.EncodeToString(random[32:])
	digest := githubStateDigest(state)
	expires := g.service.now().UTC().Add(10 * time.Minute)
	s := g.service.store
	value, err := s.key.Seal(s.stateBinding(owner, digest), []byte(verifier))
	if err != nil {
		return GitHubAuthorizationStart{}, err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO github_oauth_states(user_id,app_id,digest,redirect_uri,nonce,ciphertext,expires_at)
		SELECT $1,$2,$3,$4,$5,$6,$7 WHERE EXISTS(SELECT 1 FROM users WHERE id=$1 AND membership_state='active')
		  AND EXISTS(SELECT 1 FROM installations WHERE management_origin=$8 AND state='active')
		ON CONFLICT(user_id,app_id) DO UPDATE SET digest=excluded.digest,redirect_uri=excluded.redirect_uri,
		nonce=excluded.nonce,ciphertext=excluded.ciphertext,expires_at=excluded.expires_at,used=0`,
		owner, s.appID, digest, redirect, value.Nonce, value.Ciphertext, githubStateTime(expires), origin)
	if err := githubWrite(result, err, ErrGitHubState); err != nil {
		return GitHubAuthorizationStart{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{"client_id": {s.appID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	return GitHubAuthorizationStart{URL: "https://github.com/login/oauth/authorize?" + query.Encode(), ExpiresAt: expires}, nil
}

// CompleteAuthorization binds the issued GitHub identity to the owner who
// initiated the request. Replacing the state or revoking fences an in-flight callback.
func (g *GitHub) CompleteAuthorization(ctx context.Context, state, code, origin string) error {
	redirect, err := githubCallback(origin)
	if err != nil || len(state) != 43 || !validGitHubToken(code) {
		return ErrGitHubState
	}
	s := g.service.store
	digest := githubStateDigest(state)
	var owner string
	var value cryptostore.Value
	err = s.db.QueryRowContext(ctx, `UPDATE github_oauth_states SET used=1
		WHERE app_id=$1 AND digest=$2 AND redirect_uri=$3 AND expires_at>$4 AND used=0
		  AND EXISTS(SELECT 1 FROM users WHERE id=github_oauth_states.user_id AND membership_state='active')
		  AND EXISTS(SELECT 1 FROM installations WHERE management_origin=$5 AND state='active')
		RETURNING user_id,nonce,ciphertext`, s.appID, digest, redirect, githubStateTime(g.service.now()), origin).
		Scan(&owner, &value.Nonce, &value.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubState
	}
	if err != nil {
		return err
	}
	verifier, err := s.key.Open(s.stateBinding(owner, digest), value)
	if err != nil {
		return err
	}
	defer clear(verifier)
	pair, err := g.http.exchange(ctx, url.Values{"code": {code}, "redirect_uri": {redirect}, "code_verifier": {string(verifier)}})
	if err != nil {
		return err
	}
	providerUserID, err := g.http.userID(ctx, pair.Access)
	if err != nil {
		return err
	}
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		now := g.service.now()
		result, err := tx.ExecContext(ctx, `DELETE FROM github_oauth_states
			WHERE user_id=$1 AND app_id=$2 AND digest=$3 AND used=1 AND expires_at>$4
			  AND EXISTS(SELECT 1 FROM installations WHERE management_origin=$5 AND state='active')`,
			owner, s.appID, digest, githubStateTime(now), origin)
		if err := githubWrite(result, err, ErrGitHubState); err != nil {
			return err
		}
		return s.connectWith(ctx, tx, owner, providerUserID, pair, now)
	})
}

// RevokeAuthorization clears stored GitHub credentials and outstanding browser state.
func (g *GitHub) RevokeAuthorization(ctx context.Context, owner string) error {
	return database.Transaction(ctx, g.service.store.db, func(tx *sql.Tx) error {
		return g.RevokeAuthorizationInTx(ctx, tx, owner)
	})
}

// RevokeAuthorizationInTx clears authorization alongside the caller's session check.
func (g *GitHub) RevokeAuthorizationInTx(ctx context.Context, tx *sql.Tx, owner string) error {
	s := g.service.store
	if _, err := tx.ExecContext(ctx, "DELETE FROM github_oauth_states WHERE user_id=$1 AND app_id=$2", owner, s.appID); err != nil {
		return err
	}
	return s.revokeWith(ctx, tx, owner, g.service.now())
}

func githubCallback(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.EscapedPath() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != origin {
		return "", ErrGitHubState
	}
	u.Path = "/api/v1/git/authorization/callback"
	return u.String(), nil
}

func githubStateDigest(state string) string {
	digest := sha256.Sum256([]byte(state))
	return hex.EncodeToString(digest[:])
}

func githubStateTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func (s githubStore) stateBinding(owner, digest string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "github_oauth", Scope: "user", OwnerID: owner, RecordID: digest, Name: s.appID}
}

func (h githubHTTP) userID(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+"/user", nil)
	if err != nil {
		return "", errors.New("cannot construct GitHub identity request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	var result struct {
		ID int64 `json:"id"`
	}
	status, err := h.send(req, &result, 256<<10)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || result.ID < 1 {
		return "", errors.New("GitHub did not confirm the authorized identity")
	}
	return strconv.FormatInt(result.ID, 10), nil
}
