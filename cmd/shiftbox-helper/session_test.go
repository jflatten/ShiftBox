package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperPairingBindsLoopbackAndProof verifies that only the exact numeric
// callback and state can trigger the HTTPS exchange carrying the verifier.
func TestHelperPairingBindsLoopbackAndProof(t *testing.T) {
	const code = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const token = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"
	now := time.Now().UTC()
	var state, challenge, callback string
	tlsController := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/ssh/helper/exchange" ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Origin") != "" {
			t.Error("pairing exchange did not use the workstation HTTPS route")
			http.Error(w, "bad exchange", http.StatusBadRequest)
			return
		}
		var exchange struct {
			Code        string `json:"code"`
			State       string `json:"state"`
			Verifier    string `json:"verifier"`
			RedirectURI string `json:"redirectUri"`
		}
		if err := json.NewDecoder(r.Body).Decode(&exchange); err != nil {
			t.Error(err)
			http.Error(w, "bad exchange", http.StatusBadRequest)
			return
		}
		digest := sha256.Sum256([]byte(exchange.Verifier))
		if exchange.Code != code || exchange.State != state || exchange.RedirectURI != callback ||
			base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || exchange.Verifier == state {
			t.Error("pairing exchange did not match the browser proof and callback")
		}
		_ = json.NewEncoder(w).Encode(helperSession{Credential: token, ExpiresAt: now.Add(time.Hour), AccountID: "owner"})
	}))
	defer tlsController.Close()
	launch := func(ctx context.Context, target string) error {
		approval, err := url.Parse(target)
		if err != nil {
			return err
		}
		if approval.Scheme != "https" || approval.Path != "/account/helper/" ||
			tlsController.URL != approval.Scheme+"://"+approval.Host {
			t.Fatalf("approval URL = %q, want the authenticated HTTPS controller", target)
		}
		params := approval.Query()
		state, challenge, callback = params.Get("state"), params.Get("challenge"), params.Get("redirectUri")
		loopback, err := url.Parse(callback)
		if err != nil {
			return err
		}
		host, _, err := net.SplitHostPort(loopback.Host)
		ip, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !ip.IsLoopback() || loopback.Scheme != "http" || loopback.RawQuery != "" ||
			params.Get("verifier") != "" || state == challenge {
			t.Fatal("browser did not receive a numeric loopback and independent proof")
		}
		for _, tc := range []struct {
			path, rawQuery, overrideHost string
			status                       int
		}{
			{loopback.Path, "state=wrong&code=" + code, "", http.StatusBadRequest},
			{loopback.Path + "/other", "state=" + state + "&code=" + code, "", http.StatusBadRequest},
			{loopback.Path, "state=" + state + "&state=" + state + "&code=" + code, "", http.StatusBadRequest},
			{loopback.Path, "state=" + state + "&code=" + code, "other.test", http.StatusBadRequest},
			{loopback.Path, "state=" + state + "&code=" + code, "", http.StatusOK},
		} {
			requestURL := *loopback
			requestURL.Path, requestURL.RawQuery = tc.path, tc.rawQuery
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
			if err != nil {
				return err
			}
			req.Host = tc.overrideHost
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Errorf("pairing callback with path %q returned %d, want %d", tc.path, resp.StatusCode, tc.status)
			}
		}
		return nil
	}
	session, err := pairWithController(t.Context(), tlsController.Client(), tlsController.URL, func() time.Time { return now }, launch)
	if err != nil || session.AccountID != "owner" || session.Credential != token || !session.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("pairWithController = account %q, expiry %v, error %v, want valid scoped session", session.AccountID, session.ExpiresAt, err)
	}
}

// TestHelperSyncKeepsAliasesOnlyWhileAuthorized verifies transient discovery
// failure preserves live aliases, while expiry or a controller 401 clears them.
func TestHelperSyncKeepsAliasesOnlyWhileAuthorized(t *testing.T) {
	for _, tc := range []struct {
		name        string
		response    int
		expired     bool
		wantManaged bool
	}{
		{"unavailable", http.StatusServiceUnavailable, false, true},
		{"invalid discovery payload", http.StatusOK, false, true},
		{"revoked", http.StatusUnauthorized, false, false},
		{"expired locally", http.StatusOK, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			session := helperSession{Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour), AccountID: "owner"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.expired || r.Method != http.MethodGet || r.URL.Path != "/api/v1/ssh/workspaces" ||
					r.Header.Get("Authorization") != "Bearer "+session.Credential || r.Header.Get("Origin") != "" {
					t.Error("discovery attempted with an expired or incorrect helper scope")
				}
				w.WriteHeader(tc.response)
			}))
			defer server.Close()
			paths := sshConfigFixture(t)
			const userConfig = "Host user-work\n    HostName user.test\n"
			if err := os.WriteFile(paths.ConfigFile, []byte(userConfig), 0600); err != nil {
				t.Fatal(err)
			}
			if err := reconcileSSHConfig(paths, server.URL, session.AccountID, []sshAliasWorkspace{
				{ID: "workspace-one", Name: "Project"},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.expired {
				session.ExpiresAt = now
			}
			err := syncHelperAliases(t.Context(), server.Client(), paths, server.URL, session, func() time.Time { return now })
			if err == nil || errors.Is(err, errSSHSignInRequired) != !tc.wantManaged {
				t.Errorf("syncHelperAliases = %v, want recoverable sign-in required %v", err, !tc.wantManaged)
			}
			got := readSSHConfigFixture(t, paths.ConfigFile)
			if strings.Contains(got, "Host shiftbox-project\n") != tc.wantManaged || !strings.HasSuffix(got, userConfig) {
				t.Errorf("reconciled config managed = %t, want %t and preserved user config",
					strings.Contains(got, "Host shiftbox-project\n"), tc.wantManaged)
			}
		})
	}
}

// TestHelperSyncReconcilesDiscovery verifies authoritative items reach the
// alias writer and no bearer credential becomes part of the SSH configuration.
func TestHelperSyncReconcilesDiscovery(t *testing.T) {
	now := time.Now().UTC()
	session := helperSession{Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour), AccountID: "owner"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"eligible-workspace","name":"Asterisk * and spaces"}]}`))
	}))
	defer server.Close()
	paths := sshConfigFixture(t)
	if err := syncHelperAliases(t.Context(), server.Client(), paths, server.URL, session, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	got := readSSHConfigFixture(t, paths.ConfigFile)
	if !strings.Contains(got, "Host shiftbox-asterisk-and-spaces\n") || strings.Contains(got, session.Credential) {
		t.Error("configuration did not contain one literal alias without its helper credential")
	}
}

// TestHelperDoesNotWriteAliasesFromAnExpiredResponse checks a successful
// discovery response cannot revive SSH entries past the local validity deadline.
func TestHelperDoesNotWriteAliasesFromAnExpiredResponse(t *testing.T) {
	now := time.Now().UTC()
	session := helperSession{Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour), AccountID: "owner"}
	var expired atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		expired.Store(true)
		_, _ = w.Write([]byte(`{"items":[{"id":"workspace-one","name":"Should not appear"}]}`))
	}))
	defer server.Close()
	clock := func() time.Time {
		if expired.Load() {
			return session.ExpiresAt
		}
		return now
	}
	paths := sshConfigFixture(t)
	err := syncHelperAliases(t.Context(), server.Client(), paths, server.URL, session, clock)
	if !errors.Is(err, errSSHSignInRequired) {
		t.Errorf("syncHelperAliases after deadline = %v, want sign-in required", err)
	}
	if _, err := os.Stat(paths.ConfigFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config after response past deadline = %v, want no managed config", err)
	}
}

// TestHelperExchangeRejectsRedirect verifies that the one-time approval and
// proof verifier are never posted to a redirect target.
func TestHelperExchangeRejectsRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != helperExchangePath {
			t.Error("pairing exchange followed a redirect")
			http.Error(w, "redirected", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	if _, err := exchangeHelperCode(t.Context(), server.Client(), server.URL, "code", "state", "proof",
		"http://127.0.0.1:12345/callback", time.Now); err == nil {
		t.Fatal("exchangeHelperCode followed a redirect or accepted it as pairing")
	}
}

// TestHelperProxyRequiresCurrentAccount checks stale OpenSSH stanzas cannot
// use a credential for a different local pairing or a locally expired session.
func TestHelperProxyRequiresCurrentAccount(t *testing.T) {
	now := time.Now().UTC()
	const origin = "https://controller.test"
	session := helperSession{Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour), AccountID: "current"}
	store := &memoryHelperStore{origin: origin, session: session}
	if _, err := loadProxySession(store, origin, "previous", now); !errors.Is(err, errSSHSignInRequired) {
		t.Errorf("stale alias authorization = %v, want sign-in required", err)
	}
	if _, err := loadProxySession(store, origin, "current", session.ExpiresAt); !errors.Is(err, errSSHSignInRequired) {
		t.Errorf("locally expired alias authorization = %v, want sign-in required", err)
	}
	if got, err := loadProxySession(store, origin, "current", now); err != nil || got != session {
		t.Errorf("current proxy session = account %q, error %v, want the exact stored session", got.AccountID, err)
	}
}

// TestHelperPersistsPairingBeforeDiscovery verifies the scoped credential
// moves through the store before it is ever used for alias discovery.
func TestHelperPersistsPairingBeforeDiscovery(t *testing.T) {
	const code = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	now := time.Now().UTC()
	session := helperSession{Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour), AccountID: "owner"}
	store := &memoryHelperStore{}
	listed := make(chan struct{}, 1)
	controller := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case helperExchangePath:
			_ = json.NewEncoder(w).Encode(session)
		case helperDiscoveryPath:
			current, err := store.Load("https://" + r.Host)
			if err != nil || current != session || r.Header.Get("Authorization") != "Bearer "+session.Credential {
				t.Error("workspace discovery used the session before saving it to the credential store")
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"workspace-one","name":"Ready"}]}`))
			select {
			case listed <- struct{}{}:
			default:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	paths := sshConfigFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	launched := func(ctx context.Context, target string) error {
		parsed, err := url.Parse(target)
		if err != nil {
			return err
		}
		query := parsed.Query()
		callback := query.Get("redirectUri") + "?" + url.Values{"state": {query.Get("state")}, "code": {code}}.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback, nil)
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		return response.Body.Close()
	}
	done := make(chan error, 1)
	go func() {
		done <- runHelper(ctx, controller.Client(), paths, controller.URL, store, func() time.Time { return now }, launched, os.Stderr)
	}()
	select {
	case <-listed:
	case err := <-done:
		t.Fatalf("helper exited before listing workspaces: %v", err)
	case <-ctx.Done():
		t.Fatal("helper did not open the paired workspace listing")
	}
	for {
		config, err := os.ReadFile(paths.ConfigFile)
		if err == nil && strings.Contains(string(config), "Host shiftbox-ready\n") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("helper did not write a paired workspace alias")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("helper returned %v at cancellation, want context cancelled", err)
	}
}

type memoryHelperStore struct {
	mu      sync.Mutex
	origin  string
	session helperSession
}

func (s *memoryHelperStore) Load(origin string) (helperSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.origin != origin || s.session.Credential == "" {
		return helperSession{}, errNoHelperCredential
	}
	return s.session, nil
}

func (s *memoryHelperStore) Save(origin string, session helperSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origin, s.session = origin, session
	return nil
}

func (s *memoryHelperStore) Remove(origin string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.origin == origin {
		s.session = helperSession{}
	}
	return nil
}
