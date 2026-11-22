package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestGitHubBrowserAuthorization verifies PKCE, one-use state across
// reconstruction, and the stable GitHub identifier stored for the owner.
func TestGitHubBrowserAuthorization(t *testing.T) {
	app, db, _ := gitHubBrowserFixture(t)
	const origin = "https://management.test"
	launch, err := app.BeginAuthorization(t.Context(), "owner", origin)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(launch.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	state := query.Get("state")
	if target.Host != "github.com" || query.Get("redirect_uri") != origin+"/api/v1/git/authorization/callback" ||
		state == "" || query.Get("code_challenge_method") != "S256" || query.Has("client_secret") {
		t.Fatalf("BeginAuthorization() produced unexpected authorization parameters")
	}
	var calls int
	app.http.client.Transport = githubRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Host + r.URL.Path {
		case "github.com/login/oauth/access_token":
			if r.ParseForm() != nil {
				t.Fatal("invalid upstream OAuth form")
			}
			hash := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "fixture-code" ||
				r.PostForm.Get("redirect_uri") != query.Get("redirect_uri") ||
				base64.RawURLEncoding.EncodeToString(hash[:]) != query.Get("code_challenge") {
				t.Error("upstream exchange did not prove the original PKCE challenge")
			}
			return githubResponse(`{"access_token":"fixture-access","token_type":"bearer"}`), nil
		case "api.github.com/user":
			if r.Header.Get("Authorization") != "Bearer fixture-access" {
				t.Error("stable identity lookup did not use the issued user token")
			}
			return githubResponse(`{"id":42,"login":"changeable-login"}`), nil
		default:
			t.Fatal("unexpected upstream destination")
			return nil, nil
		}
	})
	reopened := &GitHub{service: githubService{store: app.service.store, now: app.service.now}, http: app.http}
	if err := reopened.CompleteAuthorization(t.Context(), state, "fixture-code", "https://other.test"); !errors.Is(err, ErrGitHubState) {
		t.Errorf("CompleteAuthorization(wrong origin) = %v, want state denial", err)
	}
	if err := reopened.CompleteAuthorization(t.Context(), state, "fixture-code", origin); err != nil {
		t.Fatal(err)
	}
	if err := reopened.CompleteAuthorization(t.Context(), state, "fixture-code", origin); !errors.Is(err, ErrGitHubState) || calls != 2 {
		t.Errorf("CompleteAuthorization(replay) = %v, upstream calls %d, want state denial and 2 calls", err, calls)
	}
	var userID string
	if err := db.QueryRow("SELECT provider_user_id FROM github_authorizations WHERE user_id='owner'").Scan(&userID); err != nil || userID != "42" {
		t.Errorf("stored provider ID = %q, %v, want 42", userID, err)
	}
}

// TestGitHubBrowserRevocationFencesCallback ensures a callback that was
// already exchanging a code cannot reconnect a revoked account.
func TestGitHubBrowserRevocationFencesCallback(t *testing.T) {
	app, _, now := gitHubBrowserFixture(t)
	const origin = "https://management.test"
	launch, err := app.BeginAuthorization(t.Context(), "owner", origin)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(launch.URL)
	if err != nil {
		t.Fatal(err)
	}
	app.http.client.Transport = githubRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/user" {
			if err := app.RevokeAuthorization(t.Context(), "owner"); err != nil {
				t.Fatal(err)
			}
			return githubResponse(`{"id":42}`), nil
		}
		return githubResponse(`{"access_token":"fixture-access","token_type":"bearer"}`), nil
	})
	if err := app.service.store.connect(t.Context(), "owner", "42", githubTokenFixture(now, "old"), now); err != nil {
		t.Fatal(err)
	}
	if err := app.CompleteAuthorization(t.Context(), target.Query().Get("state"), "fixture-code", origin); !errors.Is(err, ErrGitHubState) {
		t.Errorf("CompleteAuthorization(revoked during exchange) = %v, want state denial", err)
	}
	auth, err := app.service.store.load(t.Context(), "owner")
	if err != nil || auth.State != "revoked" || auth.Tokens.Access != "" {
		t.Errorf("load(revoked owner) state=%q, token=%t, err=%v, want revoked with no token", auth.State, auth.Tokens.Access != "", err)
	}
}

// TestGitHubBrowserRejectsExpiredAndSupersededState checks that neither
// an old browser tab nor an expired callback can reach GitHub's token endpoint.
func TestGitHubBrowserRejectsExpiredAndSupersededState(t *testing.T) {
	app, _, now := gitHubBrowserFixture(t)
	app.service.now = func() time.Time { return now }
	const origin = "https://management.test"
	begin := func() string {
		t.Helper()
		launch, err := app.BeginAuthorization(t.Context(), "owner", origin)
		if err != nil {
			t.Fatal(err)
		}
		target, err := url.Parse(launch.URL)
		if err != nil {
			t.Fatal(err)
		}
		return target.Query().Get("state")
	}
	app.http.client.Transport = githubRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid OAuth state reached GitHub")
		return nil, nil
	})
	first, next := begin(), begin()
	if err := app.CompleteAuthorization(t.Context(), first, "fixture-code", origin); !errors.Is(err, ErrGitHubState) {
		t.Errorf("CompleteAuthorization(superseded state) = %v, want state denial", err)
	}
	now = now.Add(10 * time.Minute)
	if err := app.CompleteAuthorization(t.Context(), next, "fixture-code", origin); !errors.Is(err, ErrGitHubState) {
		t.Errorf("CompleteAuthorization(expired state) = %v, want state denial", err)
	}
}

func gitHubBrowserFixture(t *testing.T) (*GitHub, *sql.DB, time.Time) {
	t.Helper()
	store, db, now := gitHubFixture(t)
	profile := "sqlite"
	if os.Getenv("SHIFTBOX_TEST_GITHUB_POSTGRES_URI") != "" {
		profile = "postgres"
	}
	if _, err := db.Exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES('00000000-0000-4000-8000-000000000001','active','ShiftBox',$1,'https://management.test','ide.test','preview.test','release','release','now','now',1)`, profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM installations WHERE id='00000000-0000-4000-8000-000000000001'"); err != nil {
			t.Errorf("clean up GitHub installation fixture: %v", err)
		}
	})
	app, err := NewGitHub(db, store.key, store.appID, "fixture-secret", 55, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return app, db, now
}

func githubResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

// TestGitHubProviderUsesAppCoverage verifies the token exchange and paginated
// installation repository lookup use separate credentials and fixed hosts.
func TestGitHubProviderUsesAppCoverage(t *testing.T) {
	now := time.Now().UTC()
	var calls []string
	client := &http.Client{Transport: githubRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Host+req.URL.Path+"?"+req.URL.RawQuery)
		status, body := 200, ""
		switch req.URL.Host + req.URL.Path {
		case "github.com/login/oauth/access_token":
			if req.Method != http.MethodPost || req.ParseForm() != nil ||
				req.PostForm.Get("refresh_token") != "fixture-refresh" || req.PostForm.Get("client_secret") != "fixture-app-secret" ||
				req.PostForm.Get("grant_type") != "refresh_token" || req.URL.RawQuery != "" {
				t.Error("unexpected GitHub refresh exchange")
			}
			body = `{"access_token":"fixture-access","refresh_token":"fixture-next-refresh","expires_in":3600,"refresh_token_expires_in":86400,"token_type":"bearer"}`
		case "api.github.com/user/installations/55/repositories":
			if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer fixture-access" {
				t.Error("repository coverage did not use the current owner credential")
			}
			body = `{"total_count":101,"repositories":[{"full_name":"other/repo"}]}`
			if req.URL.Query().Get("page") == "2" {
				body = `{"total_count":101,"repositories":[{"full_name":"Example/Private"}]}`
			}
		default:
			t.Errorf("unexpected upstream destination %s", req.URL.Host+req.URL.Path)
			status = 400
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	endpoint := githubHTTP{client: client, clientID: "fixture-app", clientSecret: "fixture-app-secret", installationID: 55,
		now: func() time.Time { return now }}
	pair, err := endpoint.refresh(t.Context(), "fixture-refresh")
	if err != nil || pair.Access != "fixture-access" || !pair.AccessExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("refresh() returned unexpected pair or error %v", err)
	}
	if err := endpoint.covers(t.Context(), pair.Access, "https://github.com/example/private.git"); err != nil || len(calls) != 3 {
		t.Errorf("covers(known repository) = %v, calls %v, want success after two pages", err, calls)
	}
}

type githubRoundTrip func(*http.Request) (*http.Response, error)

func (f githubRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// TestGitHubRefreshCoordinatesAndChecksCoverage checks one refresh under
// concurrent guest requests and the repository filter on the resulting token.
func TestGitHubRefreshCoordinatesAndChecksCoverage(t *testing.T) {
	store, db, now := gitHubFixture(t)
	var refreshes atomic.Int32
	provider := githubProvider{
		refresh: func(context.Context, string) (githubTokens, error) {
			refreshes.Add(1)
			return githubTokenFixture(now, "replacement"), nil
		},
		covers: func(_ context.Context, token, repo string) error {
			if token != "access-replacement" || repo != "https://github.com/example/private.git" {
				return ErrGitHubRepositoryDenied
			}
			return nil
		},
	}
	service := githubService{store: store, provider: provider, now: func() time.Time { return now }}
	initial := githubTokenFixture(now, "expired")
	initial.AccessExpiresAt = now.Add(-time.Second)
	if err := store.connect(t.Context(), "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(func() {
			token, err := service.accessToken(t.Context(), "owner", "https://github.com/example/private.git")
			if err != nil || token != "access-replacement" {
				t.Errorf("accessToken(covered repo) failed, err = %v", err)
			}
		})
	}
	calls.Wait()
	if got := refreshes.Load(); got != 1 {
		t.Errorf("concurrent refresh calls = %d, want 1", got)
	}
	if token, err := service.accessToken(t.Context(), "owner", "https://github.com/example/other.git"); token != "" || !errors.Is(err, ErrGitHubRepositoryDenied) {
		t.Errorf("accessToken(uncovered repo) returned token=%t, error=%v, want no token and repository denial", token != "", err)
	}
	var encrypted []byte
	if err := db.QueryRow("SELECT ciphertext FROM github_authorizations WHERE user_id='owner'").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"access-replacement", "refresh-replacement"} {
		if bytes.Contains(encrypted, []byte(value)) {
			t.Error("stored GitHub credential contained plaintext")
		}
	}
}

// TestGitHubRefreshCannotUndoRevoke covers revocation while GitHub is
// rotating the token pair and rejects a mismatched provider identity.
func TestGitHubRefreshCannotUndoRevoke(t *testing.T) {
	store, _, now := gitHubFixture(t)
	initial := githubTokenFixture(now, "expired")
	initial.AccessExpiresAt = now.Add(-time.Second)
	if err := store.connect(t.Context(), "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	if err := store.connect(t.Context(), "owner", "43", initial, now); !errors.Is(err, ErrGitHubIdentityConflict) {
		t.Fatalf("connect(changed stable identity) = %v, want identity conflict", err)
	}
	service := githubService{store: store, now: func() time.Time { return now }, provider: githubProvider{
		refresh: func(ctx context.Context, _ string) (githubTokens, error) {
			if err := store.revoke(ctx, "owner", now); err != nil {
				t.Fatal(err)
			}
			return githubTokenFixture(now, "stale"), nil
		},
		covers: func(context.Context, string, string) error { return nil },
	}}
	if token, err := service.accessToken(t.Context(), "owner", "https://github.com/example/private.git"); token != "" || !errors.Is(err, ErrGitHubReconnect) {
		t.Errorf("accessToken(revoked mid-refresh) returned token=%t, error=%v, want reconnect", token != "", err)
	}
}

// TestGitHubRefreshGenerationFencesReconnectedAuthorization checks that an old
// provider response cannot complete a new refresh after revoke and reconnect.
func TestGitHubRefreshGenerationFencesReconnectedAuthorization(t *testing.T) {
	store, db, now := gitHubFixture(t)
	ctx := t.Context()
	initial := githubTokenFixture(now, "initial")
	if err := store.connect(ctx, "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE github_authorizations SET provider='example.test'"); err == nil {
		t.Error("Git authorization schema accepted a provider other than github.com")
	}
	claim := func() int64 {
		t.Helper()
		current, err := store.load(ctx, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.claimRefresh(ctx, "owner", current.Version, now); err != nil {
			t.Fatal(err)
		}
		var generation int64
		if err := db.QueryRowContext(ctx, "SELECT refresh_generation FROM github_authorizations WHERE user_id='owner'").Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	first := claim()
	if err := store.revoke(ctx, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := store.connect(ctx, "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if second <= first || first <= 0 {
		t.Fatalf("refresh generations %d then %d, want increasing positive generations", first, second)
	}
	if err := store.finishRefresh(ctx, "owner", first, "connected", initial, now); !errors.Is(err, ErrGitHubReconnect) {
		t.Errorf("finishRefresh(stale generation) = %v, want reconnect", err)
	}
	if err := store.finishRefresh(ctx, "owner", second, "connected", initial, now); err != nil {
		t.Errorf("finishRefresh(current generation) = %v, want success", err)
	}
}

// TestGitHubPermanentRefreshFailure persists reconnect-required across
// service reconstruction and discards the unusable token pair.
func TestGitHubPermanentRefreshFailure(t *testing.T) {
	store, db, now := gitHubFixture(t)
	initial := githubTokenFixture(now, "expired")
	initial.AccessExpiresAt = now
	if err := store.connect(t.Context(), "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	var calls int
	provider := githubProvider{
		refresh: func(context.Context, string) (githubTokens, error) {
			calls++
			return githubTokens{}, ErrGitHubReconnect
		},
		covers: func(context.Context, string, string) error { return nil },
	}
	for range 2 {
		service := githubService{store: store, provider: provider, now: func() time.Time { return now }}
		if token, err := service.accessToken(t.Context(), "owner", "https://github.com/example/private.git"); token != "" || !errors.Is(err, ErrGitHubReconnect) {
			t.Errorf("accessToken(permanent failure) returned token=%t, error=%v, want reconnect", token != "", err)
		}
	}
	var state string
	var ciphertext []byte
	if err := db.QueryRow("SELECT state,ciphertext FROM github_authorizations WHERE user_id='owner'").Scan(&state, &ciphertext); err != nil ||
		state != "reconnect_required" || len(ciphertext) != 0 || calls != 1 {
		t.Errorf("permanent failure stored state=%q, ciphertext bytes=%d, refreshes=%d, err=%v, want reconnect_required, 0, 1", state, len(ciphertext), calls, err)
	}
}

func gitHubFixture(t *testing.T) (githubStore, *sql.DB, time.Time) {
	t.Helper()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "github.db")}
	if uri := os.Getenv("SHIFTBOX_TEST_GITHUB_POSTGRES_URI"); uri != "" {
		cfg = database.Config{Profile: "postgres", PostgresURI: uri, Schema: "shiftbox_test"}
	}
	db, err := database.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(t.Context(), db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DELETE FROM github_oauth_states WHERE user_id='owner'; DELETE FROM github_authorizations WHERE user_id='owner'; DELETE FROM users WHERE id='owner'"); err != nil {
			t.Errorf("clean up GitHub fixture: %v", err)
		}
	})
	if _, err := db.Exec(`INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('owner','owner@example.test','member','active','local','setup','now','now',1)`); err != nil {
		t.Fatal(err)
	}
	key, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return githubStore{db: db, key: key, appID: "fixture-app"}, db, time.Now().UTC()
}

func githubTokenFixture(now time.Time, suffix string) githubTokens {
	return githubTokens{
		Access: "access-" + suffix, Refresh: "refresh-" + suffix,
		AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(24 * time.Hour),
	}
}
