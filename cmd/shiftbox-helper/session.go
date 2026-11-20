package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	helperExchangePath  = "/api/v1/ssh/helper/exchange"
	helperDiscoveryPath = "/api/v1/ssh/workspaces"
	helperCallbackPath  = "/callback"
	helperPairTimeout   = 5 * time.Minute
	helperPollInterval  = 30 * time.Second
)

var errNoHelperCredential = errors.New("workspace helper credential is missing")

type helperSession struct {
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expiresAt"`
	AccountID  string    `json:"accountId"`
}

type helperCredentialStore interface {
	Load(origin string) (helperSession, error)
	Save(origin string, session helperSession) error
	Remove(origin string) error
}

func (s helperSession) validAt(now time.Time) bool {
	return helperOpaque(s.Credential) && s.AccountID != "" && len(s.AccountID) <= 128 &&
		strings.IndexFunc(s.AccountID, unicode.IsControl) == -1 && now.Before(s.ExpiresAt)
}

// runHelper keeps the one locally paired account's aliases synchronized. The
// controller separately checks every attempted SSH connection.
func runHelper(
	ctx context.Context,
	client *http.Client,
	paths sshConfigPaths,
	origin string,
	store helperCredentialStore,
	now func() time.Time,
	openBrowser func(context.Context, string) error,
	stderr io.Writer,
) error {
	session, err := store.Load(origin)
	if err != nil && !errors.Is(err, errNoHelperCredential) {
		clearErr := reconcileSSHConfig(paths, origin, "", nil)
		return errors.Join(err, clearErr)
	}
	for ctx.Err() == nil {
		started := now()
		err := syncHelperAliases(ctx, client, paths, origin, session, now)
		if errors.Is(err, errSSHSignInRequired) {
			if session.Credential != "" {
				if err := store.Remove(origin); err != nil {
					return err
				}
			}
			session, err = pairWithController(ctx, client, origin, now, openBrowser)
			if err != nil {
				return err
			}
			if err := store.Save(origin, session); err != nil {
				return err
			}
			continue
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(stderr, "ShiftBox workspace discovery:", err)
		}
		delay := helperPollInterval
		if remaining := session.ExpiresAt.Sub(now()); remaining > 0 {
			delay = min(delay, remaining)
		} else if session.validAt(started) {
			// A slow request can finish just as the known session expires.
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func loadProxySession(store helperCredentialStore, origin, accountID string, now time.Time) (helperSession, error) {
	session, err := store.Load(origin)
	if errors.Is(err, errNoHelperCredential) {
		return helperSession{}, errSSHSignInRequired
	}
	if err != nil {
		return helperSession{}, err
	}
	if session.AccountID != accountID || !session.validAt(now) {
		return helperSession{}, errSSHSignInRequired
	}
	return session, nil
}

func pairWithController(
	ctx context.Context,
	client *http.Client,
	origin string,
	now func() time.Time,
	openBrowser func(context.Context, string) error,
) (helperSession, error) {
	origin, err := canonicalSSHOrigin(origin)
	if err != nil {
		return helperSession{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, helperPairTimeout)
	defer cancel()
	state, err := randomHelperSecret()
	if err != nil {
		return helperSession{}, err
	}
	verifier, err := randomHelperSecret()
	if err != nil {
		return helperSession{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return helperSession{}, fmt.Errorf("listen for the local ShiftBox sign-in callback: %w", err)
	}
	defer listener.Close()
	host := listener.Addr().String()
	redirectURI := "http://" + host + helperCallbackPath
	codes := make(chan string, 1)
	server := &http.Server{
		Handler:           handleHelperCallback(host, state, codes),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       10 * time.Second,
		MaxHeaderBytes:    8192,
	}
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		<-done
	}()
	approval := origin + "/account/helper/?" + url.Values{
		"state": {state}, "challenge": {challenge}, "redirectUri": {redirectURI},
	}.Encode()
	if err := openBrowser(ctx, approval); err != nil {
		return helperSession{}, fmt.Errorf("open ShiftBox sign-in in the browser: %w", err)
	}
	select {
	case code := <-codes:
		return exchangeHelperCode(ctx, client, origin, code, state, verifier, redirectURI, now)
	case <-done:
		return helperSession{}, fmt.Errorf("serve the local ShiftBox sign-in callback: %w", serveErr)
	case <-ctx.Done():
		return helperSession{}, fmt.Errorf("wait for ShiftBox browser sign-in: %w", ctx.Err())
	}
}

func handleHelperCallback(host, state string, codes chan<- string) http.Handler {
	var consumed atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		query, err := url.ParseQuery(r.URL.RawQuery)
		code := query.Get("code")
		if r.Method != http.MethodGet || r.Host != host || r.URL.IsAbs() || r.URL.EscapedPath() != helperCallbackPath ||
			err != nil || len(query) != 2 || len(query["code"]) != 1 || len(query["state"]) != 1 ||
			!helperOpaque(code) || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 ||
			!consumed.CompareAndSwap(false, true) {
			http.Error(w, "Invalid local ShiftBox sign-in callback", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Approval received. You can close this tab and return to ShiftBox Local Helper.\n")
		_ = http.NewResponseController(w).Flush()
		codes <- code
	})
}

func exchangeHelperCode(
	ctx context.Context, client *http.Client, origin, code, state, verifier, redirectURI string, now func() time.Time,
) (helperSession, error) {
	body, err := json.Marshal(struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		Verifier    string `json:"verifier"`
		RedirectURI string `json:"redirectUri"`
	}{Code: code, State: state, Verifier: verifier, RedirectURI: redirectURI})
	if err != nil {
		return helperSession{}, err
	}
	defer clear(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+helperExchangePath, bytes.NewReader(body))
	if err != nil {
		return helperSession{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := helperHTTPClient(client).Do(request)
	if err != nil {
		return helperSession{}, fmt.Errorf("exchange ShiftBox helper approval: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return helperSession{}, fmt.Errorf("ShiftBox did not accept the pairing approval (HTTP %d); open the helper to try again", response.StatusCode)
	}
	var session helperSession
	if err := readHelperResponse(response.Body, &session, 8<<10); err != nil || !session.validAt(now()) {
		return helperSession{}, errors.New("ShiftBox returned an invalid helper session")
	}
	return session, nil
}

func syncHelperAliases(ctx context.Context, client *http.Client, paths sshConfigPaths, origin string, session helperSession, now func() time.Time) error {
	origin, err := canonicalSSHOrigin(origin)
	if err != nil {
		return err
	}
	signInRequired := func() error {
		if err := reconcileSSHConfig(paths, origin, "", nil); err != nil {
			return fmt.Errorf("clear expired ShiftBox aliases: %w", err)
		}
		return errSSHSignInRequired
	}
	if !session.validAt(now()) {
		return signInRequired()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+helperDiscoveryPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+session.Credential)
	response, err := helperHTTPClient(client).Do(request)
	if err != nil {
		if !session.validAt(now()) {
			return signInRequired()
		}
		return fmt.Errorf("list eligible workspaces: %w", err)
	}
	defer response.Body.Close()
	if !session.validAt(now()) {
		return signInRequired()
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return signInRequired()
	case http.StatusOK:
	default:
		return fmt.Errorf("ShiftBox could not list eligible workspaces (HTTP %d)", response.StatusCode)
	}
	var discovery struct {
		Items *[]sshAliasWorkspace `json:"items"`
	}
	if err := readHelperResponse(response.Body, &discovery, 1<<20); err != nil || discovery.Items == nil {
		return errors.New("ShiftBox returned an invalid eligible workspace list")
	}
	if !session.validAt(now()) {
		return signInRequired()
	}
	return reconcileSSHConfig(paths, origin, session.AccountID, *discovery.Items)
}

func helperHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	return &http.Client{
		Transport: client.Transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func readHelperResponse(r io.Reader, value any, maxSize int64) error {
	data, err := io.ReadAll(io.LimitReader(r, maxSize+1))
	if err != nil {
		return err
	}
	defer clear(data)
	if int64(len(data)) > maxSize {
		return errors.New("workspace helper response exceeded its size limit")
	}
	return json.Unmarshal(data, value)
}

func randomHelperSecret() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

func helperOpaque(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(32) {
		return false
	}
	var out [32]byte
	n, err := base64.RawURLEncoding.Strict().Decode(out[:], []byte(value))
	return err == nil && n == len(out)
}
