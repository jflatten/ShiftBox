package identity

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
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
		if _, err := db.Exec("DELETE FROM github_authorizations WHERE user_id='owner'; DELETE FROM users WHERE id='owner'"); err != nil {
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
