package guest

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestLocalSecretRead goes through the Unix socket twice and shows that a
// controller outage after a successful lookup cannot return a cached value.
func TestLocalSecretRead(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("secret request method = %s, want POST", r.Method)
		}
		switch r.URL.Path {
		case GitAccessTokenPath:
			switch r.Header.Get(GitRepositoryHeader) {
			case "https://github.com/example/project":
				_, _ = w.Write([]byte(`{"accessToken":"owner-token-fixture"}`))
			case "https://github.com/example/reconnect":
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"code":"git.reconnect_required"}`))
			default:
				w.WriteHeader(403)
			}
		case SecretResolvePrefix + "MISSING:resolve":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"secret.not_found"}`))
		case SecretResolvePrefix + "DENIED:resolve":
			w.WriteHeader(403)
		case SecretResolvePrefix + "TOKEN:resolve":
			_, _ = w.Write([]byte(`{"name":"TOKEN","value":"","source":"user","retrievedAt":"2026-11-14T00:00:00Z"}`))
		default:
			t.Errorf("unexpected secret path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	path := filepath.Join(t.TempDir(), "secrets.sock")
	stop, err := listenSecretSocket(ctx, path, upstream.Client(), upstream.URL+pollPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	value, err := ReadLocalSecret(ctx, path, "TOKEN")
	if err != nil || value != "" {
		t.Fatalf("local secret = %q, %v, want explicit empty", value, err)
	}
	for name, want := range map[string]error{"MISSING": sql.ErrNoRows, "DENIED": ErrGuestUnauthorized} {
		if value, err := ReadLocalSecret(ctx, path, name); !errors.Is(err, want) || value != "" {
			t.Fatalf("local secret %s returned error %v, want %v and no value", name, err, want)
		}
	}
	if token, err := ReadLocalGitToken(ctx, path, "https://github.com/example/project"); err != nil || token != "owner-token-fixture" {
		t.Fatalf("local Git credential returned error %v, want owner token", err)
	}
	for repo, want := range map[string]error{"another": ErrGuestUnauthorized, "reconnect": ErrGitReconnect} {
		if token, err := ReadLocalGitToken(ctx, path, "https://github.com/example/"+repo); !errors.Is(err, want) || token != "" {
			t.Fatalf("local Git credential for %s = %v, want %v and no token", repo, err, want)
		}
	}
	upstream.Close()
	value, err = ReadLocalSecret(ctx, path, "TOKEN")
	if err == nil || errors.Is(err, sql.ErrNoRows) || value != "" {
		t.Fatalf("local secret after outage = %q, %v, want unavailable", value, err)
	}
}
