package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/template"
	"golang.org/x/sync/singleflight"
)

var (
	// ErrGitHubReconnect requires a new browser authorization.
	ErrGitHubReconnect = errors.New("GitHub authorization requires reconnection")
	// ErrGitHubRepositoryDenied means the user and app lack repository coverage.
	ErrGitHubRepositoryDenied = errors.New("GitHub authorization does not cover the repository")
	// ErrGitHubIdentityConflict prevents replacing the linked provider account.
	ErrGitHubIdentityConflict = errors.New("GitHub account does not match the linked identity")
)

const githubRefreshTimeout = 15 * time.Second

type githubProvider struct {
	refresh func(context.Context, string) (githubTokens, error)
	covers  func(context.Context, string, string) error
}

type githubService struct {
	store    githubStore
	provider githubProvider
	now      func() time.Time
	refresh  singleflight.Group
}

// GitHub manages owner-authorized Git credentials for one configured App
// installation. Only AccessToken exposes a current, repository-checked token.
type GitHub struct{ service githubService }

// NewGitHub binds credential storage to the installation key and GitHub App.
func NewGitHub(db *sql.DB, key *cryptostore.Store, clientID, clientSecret string, installationID int64, now func() time.Time) (*GitHub, error) {
	if db == nil || key == nil || !validGitHubToken(clientID) || !validGitHubToken(clientSecret) || installationID < 1 || now == nil {
		return nil, errors.New("invalid GitHub App configuration")
	}
	endpoint := githubHTTP{
		client: &http.Client{Timeout: githubRefreshTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		clientID: clientID, clientSecret: clientSecret, installationID: installationID, now: now,
	}
	return &GitHub{service: githubService{store: githubStore{db: db, key: key, appID: clientID},
		provider: githubProvider{refresh: endpoint.refresh, covers: endpoint.covers}, now: now}}, nil
}

// AccessToken returns the current owner token after checking repository coverage.
func (g *GitHub) AccessToken(ctx context.Context, owner, repository string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*githubRefreshTimeout)
	defer cancel()
	return g.service.accessToken(ctx, owner, repository)
}

func (g *githubService) accessToken(ctx context.Context, owner, repository string) (string, error) {
	if !template.ValidSource(repository, "devcontainer.json") {
		return "", ErrGitHubRepositoryDenied
	}
	result := g.refresh.DoChan(owner, func() (any, error) {
		// The other waiters still need an answer if the first request disconnects.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubRefreshTimeout)
		defer cancel()
		return g.current(refreshCtx, owner)
	})
	var auth githubAuthorization
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case got := <-result:
		if got.Err != nil {
			return "", got.Err
		}
		auth = got.Val.(githubAuthorization)
	}

	if err := g.provider.covers(ctx, auth.Tokens.Access, repository); err != nil {
		if errors.Is(err, ErrGitHubReconnect) {
			return "", errors.Join(err, g.store.requireReconnect(ctx, owner, auth.Version, g.now()))
		}
		return "", err
	}
	active, err := g.store.activeVersion(ctx, owner, auth.Version)
	if err != nil {
		return "", err
	}
	if !active || !auth.Tokens.currentAt(g.now()) {
		return "", ErrGitHubReconnect
	}
	return auth.Tokens.Access, nil
}

func (g *githubService) current(ctx context.Context, owner string) (githubAuthorization, error) {
	auth, err := g.store.load(ctx, owner)
	if err != nil {
		return githubAuthorization{}, err
	}
	now := g.now()
	switch auth.State {
	case "connected":
		if auth.Tokens.currentAt(now) {
			return auth, nil
		}
	case "refreshing":
		// A previous process may have stopped after GitHub rotated the pair.
		// Do not repeat an uncertain exchange after its bounded request expired.
		if now.Before(auth.UpdatedAt.Add(githubRefreshTimeout)) {
			return githubAuthorization{}, errors.New("GitHub refresh is already in progress")
		}
		err := g.store.finishRefresh(ctx, owner, auth.Version, "reconnect_required", githubTokens{}, now)
		return githubAuthorization{}, errors.Join(ErrGitHubReconnect, err)
	default:
		return githubAuthorization{}, ErrGitHubReconnect
	}

	if err := g.store.claimRefresh(ctx, owner, auth.Version, now); err != nil {
		return githubAuthorization{}, err
	}
	version := auth.Version + 1
	pair, err := g.rotate(ctx, auth.Tokens, now)
	// Even when the upstream request times out, record its outcome under a
	// bounded independent context so a restart sees the current state.
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubRefreshTimeout)
	defer cancel()
	state := "connected"
	switch {
	case errors.Is(err, ErrGitHubReconnect):
		state = "reconnect_required"
		pair = githubTokens{}
	case err != nil:
		pair = auth.Tokens
	}
	if writeErr := g.store.finishRefresh(commitCtx, owner, version, state, pair, g.now()); writeErr != nil {
		return githubAuthorization{}, writeErr
	}
	if err != nil {
		return githubAuthorization{}, err
	}
	return githubAuthorization{State: state, Version: version + 1, Tokens: pair}, nil
}

func (g *githubService) rotate(ctx context.Context, previous githubTokens, now time.Time) (githubTokens, error) {
	if !validGitHubToken(previous.Refresh) || !now.Before(previous.RefreshExpiresAt) {
		return githubTokens{}, ErrGitHubReconnect
	}
	pair, err := g.provider.refresh(ctx, previous.Refresh)
	if err != nil {
		return githubTokens{}, err
	}
	if !pair.usable(g.now()) || !pair.currentAt(g.now()) || pair.Refresh == "" {
		return githubTokens{}, ErrGitHubReconnect
	}
	return pair, nil
}

func (t githubTokens) usable(now time.Time) bool {
	if !validGitHubToken(t.Access) {
		return false
	}
	if t.AccessExpiresAt.IsZero() {
		return t.Refresh == "" && t.RefreshExpiresAt.IsZero()
	}
	return validGitHubToken(t.Refresh) && t.Refresh != t.Access && now.Before(t.RefreshExpiresAt)
}

func (t githubTokens) currentAt(now time.Time) bool {
	return t.AccessExpiresAt.IsZero() || now.Before(t.AccessExpiresAt)
}

func validGitHubToken(token string) bool {
	return token != "" && len(token) <= 4096 && !strings.ContainsAny(token, " \r\n\t\x00")
}
