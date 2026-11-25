package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

type githubStore struct {
	db    *sql.DB
	key   *cryptostore.Store
	appID string
}

type githubAuthorization struct {
	State             string
	Version           int64
	RefreshGeneration int64
	UpdatedAt         time.Time
	Tokens            githubTokens
}

type githubTokens struct {
	Access, Refresh                   string
	AccessExpiresAt, RefreshExpiresAt time.Time
}

func (s githubStore) connect(ctx context.Context, owner, providerUserID string, tokens githubTokens, now time.Time) error {
	return s.connectWith(ctx, s.db, owner, providerUserID, tokens, now)
}

type githubWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s githubStore) connectWith(ctx context.Context, db githubWriter, owner, providerUserID string, tokens githubTokens, now time.Time) error {
	if owner == "" || providerUserID == "" || !tokens.usable(now) {
		return errors.New("invalid GitHub authorization")
	}
	value, err := s.seal(owner, tokens)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO github_authorizations(user_id,app_id,provider_user_id,state,nonce,ciphertext,
		access_expires_at,refresh_expires_at,updated_at,version)
		SELECT $1,$2,$3,'connected',$4,$5,$6,$7,$8,1
		WHERE EXISTS(SELECT 1 FROM users WHERE id=$1 AND membership_state='active')
		ON CONFLICT(user_id,app_id) DO UPDATE SET state='connected',nonce=excluded.nonce,ciphertext=excluded.ciphertext,
		access_expires_at=excluded.access_expires_at,refresh_expires_at=excluded.refresh_expires_at,
		updated_at=excluded.updated_at,version=github_authorizations.version+1
		WHERE github_authorizations.provider_user_id=excluded.provider_user_id`,
		owner, s.appID, providerUserID, value.Nonce, value.Ciphertext,
		gitHubTimestamp(tokens.AccessExpiresAt), gitHubTimestamp(tokens.RefreshExpiresAt), gitHubTimestamp(now))
	return githubWrite(result, err, ErrGitHubIdentityConflict)
}

func (s githubStore) load(ctx context.Context, owner string) (githubAuthorization, error) {
	var auth githubAuthorization
	var value cryptostore.Value
	var stamp string
	err := s.db.QueryRowContext(ctx, `SELECT a.state,a.version,a.refresh_generation,a.updated_at,a.nonce,a.ciphertext
		FROM github_authorizations a JOIN users u ON u.id=a.user_id
		WHERE a.user_id=$1 AND a.app_id=$2 AND u.membership_state='active'`, owner, s.appID).
		Scan(&auth.State, &auth.Version, &auth.RefreshGeneration, &stamp, &value.Nonce, &value.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return githubAuthorization{}, ErrGitHubReconnect
	}
	if err != nil {
		return githubAuthorization{}, err
	}
	auth.UpdatedAt, err = time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return githubAuthorization{}, err
	}
	if auth.State == "connected" || auth.State == "refreshing" {
		raw, err := s.key.Open(s.binding(owner), value)
		if err != nil {
			return githubAuthorization{}, err
		}
		defer clear(raw)
		if json.Unmarshal(raw, &auth.Tokens) != nil {
			return githubAuthorization{}, errors.New("invalid stored GitHub credential")
		}
	}
	return auth, nil
}

func (s githubStore) claimRefresh(ctx context.Context, owner string, version int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE github_authorizations SET state='refreshing',refresh_generation=refresh_generation+1,version=version+1,updated_at=$1
		WHERE user_id=$2 AND app_id=$3 AND version=$4 AND state='connected'
		  AND EXISTS(SELECT 1 FROM users WHERE id=$2 AND membership_state='active')`,
		gitHubTimestamp(now), owner, s.appID, version)
	return githubWrite(result, err, ErrGitHubReconnect)
}

func (s githubStore) finishRefresh(ctx context.Context, owner string, generation int64, state string, tokens githubTokens, now time.Time) error {
	value := cryptostore.Value{Nonce: []byte{}, Ciphertext: []byte{}}
	if state == "connected" {
		var err error
		value, err = s.seal(owner, tokens)
		if err != nil {
			return err
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE github_authorizations SET state=$1,nonce=$2,ciphertext=$3,
		access_expires_at=$4,refresh_expires_at=$5,updated_at=$6,version=version+1
		WHERE user_id=$7 AND app_id=$8 AND refresh_generation=$9 AND state='refreshing'`,
		state, value.Nonce, value.Ciphertext, gitHubTimestamp(tokens.AccessExpiresAt), gitHubTimestamp(tokens.RefreshExpiresAt),
		gitHubTimestamp(now), owner, s.appID, generation)
	return githubWrite(result, err, ErrGitHubReconnect)
}

func (s githubStore) revoke(ctx context.Context, owner string, now time.Time) error {
	return s.revokeWith(ctx, s.db, owner, now)
}

func (s githubStore) revokeWith(ctx context.Context, db githubWriter, owner string, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE github_authorizations SET state='revoked',nonce=$1,ciphertext=$1,
		access_expires_at='',refresh_expires_at='',updated_at=$2,version=version+1
		WHERE user_id=$3 AND app_id=$4 AND state<>'revoked'`, []byte{}, gitHubTimestamp(now), owner, s.appID)
	return err
}

func (s githubStore) requireReconnect(ctx context.Context, owner string, version int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE github_authorizations SET state='reconnect_required',nonce=$1,ciphertext=$1,
		access_expires_at='',refresh_expires_at='',updated_at=$2,version=version+1
		WHERE user_id=$3 AND app_id=$4 AND version=$5 AND state='connected'`, []byte{}, gitHubTimestamp(now), owner, s.appID, version)
	return err
}

// InvalidateGitHubAppAuthorizationsInTx clears credentials and unfinished
// OAuth flows for an installation that is being removed or replaced.
func InvalidateGitHubAppAuthorizationsInTx(ctx context.Context, tx *sql.Tx, appID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM github_oauth_states WHERE app_id=$1", appID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE github_authorizations SET state='reconnect_required',nonce=$1,ciphertext=$1,
		access_expires_at='',refresh_expires_at='',updated_at=$2,version=version+1
		WHERE app_id=$3 AND state IN ('connected','refreshing')`, []byte{}, gitHubTimestamp(now), appID)
	return err
}

func (s githubStore) activeVersion(ctx context.Context, owner string, version int64) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM github_authorizations a JOIN users u ON u.id=a.user_id
		WHERE a.user_id=$1 AND a.app_id=$2 AND a.version=$3 AND a.state='connected' AND u.membership_state='active')`,
		owner, s.appID, version).Scan(&active)
	return active, err
}

func (s githubStore) seal(owner string, tokens githubTokens) (cryptostore.Value, error) {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return cryptostore.Value{}, err
	}
	defer clear(raw)
	return s.key.Seal(s.binding(owner), raw)
}

func (s githubStore) binding(owner string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "github_authorization", Scope: "user", OwnerID: owner, RecordID: s.appID, Name: "tokens"}
}

func githubWrite(result sql.Result, err, conflict error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return conflict
	}
	return err
}

func gitHubTimestamp(stamp time.Time) string {
	if stamp.IsZero() {
		return ""
	}
	return stamp.UTC().Format(time.RFC3339Nano)
}
