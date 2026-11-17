package guest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/secret"
)

// SecretSocketPath is shared between the agent and the workspace's tooling.
const SecretSocketPath = credentialDirectory + "/secrets.sock"

const secretReplyLimit = 384 << 10

type secretReply struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}

// SecretKey describes one available secret name and effective display scope.
type SecretKey = secret.Key

type secretListReply struct {
	Keys []SecretKey `json:"keys"`
}

func listenSecretSocket(ctx context.Context, path string, client *http.Client, pollEndpoint string) (func(), error) {
	if !validPollEndpoint(pollEndpoint) {
		return nil, errors.New("invalid controller secret endpoint")
	}
	endpoint, err := url.Parse(pollEndpoint)
	if err != nil {
		return nil, errors.New("invalid controller secret endpoint")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("credential socket path is occupied")
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			conn.Close()
			return nil, errors.New("credential socket is already serving")
		}
		if err := os.Remove(path); err != nil {
			return nil, errors.New("cannot remove stale credential socket")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect credential socket")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, errors.New("cannot bind credential socket")
	}
	// Any process in this workspace may retrieve effective secrets. The
	// containing directory is controlled by the guest supervisor.
	if err := os.Chmod(path, 0666); err != nil {
		listener.Close()
		return nil, errors.New("cannot set credential socket permissions")
	}
	socketCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return socketCtx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.RequestURI() == "/secrets" && r.Method == http.MethodGet && r.ContentLength == 0 {
				localSecretList(w, r, client, endpoint)
				return
			}
			if r.URL.RequestURI() == "/git" && r.Method == http.MethodPost && r.ContentLength == 0 {
				localGitToken(w, r, client, pollEndpoint)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if r.Method != http.MethodPost || r.URL.RequestURI() != "/"+name || !secret.ValidName(name) || r.ContentLength != 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			target := *endpoint
			target.Path, target.RawPath = SecretResolvePrefix+name+":resolve", ""
			req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), nil)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			response, err := client.Do(req)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			defer response.Body.Close()
			switch response.StatusCode {
			case http.StatusOK:
				value, err := decodeSecretReply(response.Body, name)
				if err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(secretReply{Name: name, Value: &value})
			case http.StatusUnauthorized, http.StatusForbidden:
				w.WriteHeader(http.StatusForbidden)
			case http.StatusNotFound:
				var problem struct {
					Code string `json:"code"`
				}
				if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&problem); err == nil && problem.Code == "secret.not_found" {
					w.WriteHeader(http.StatusNotFound)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	return func() {
		cancel()
		_ = server.Close()
		_ = listener.Close()
		<-done
	}, nil
}

// ListLocalSecrets requests current effective keys through the local agent.
// It returns names and scopes only, with no cached result after a failed lookup.
func ListLocalSecrets(ctx context.Context, socket string) ([]SecretKey, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://shiftbox.local/secrets", nil)
	if err != nil {
		return nil, err
	}
	reply, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer reply.Body.Close()
	if reply.StatusCode == http.StatusForbidden {
		return nil, ErrGuestUnauthorized
	}
	if reply.StatusCode != http.StatusOK {
		return nil, errors.New("workspace secret service is unavailable")
	}
	return decodeSecretListReply(reply.Body)
}

func localSecretList(w http.ResponseWriter, r *http.Request, client *http.Client, endpoint *url.URL) {
	target := *endpoint
	target.Path, target.RawPath = SecretListPath, ""
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	response, err := client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if response.StatusCode != http.StatusOK {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	keys, err := decodeSecretListReply(response.Body)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(secretListReply{Keys: keys})
}

func decodeSecretListReply(body io.Reader) ([]SecretKey, error) {
	raw, err := io.ReadAll(io.LimitReader(body, secretReplyLimit+1))
	defer clear(raw)
	if err != nil || len(raw) > secretReplyLimit {
		return nil, errors.New("invalid secret list response")
	}
	var reply secretListReply
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil || reply.Keys == nil {
		return nil, errors.New("invalid secret list response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid secret list response")
	}
	seen := make(map[string]bool, len(reply.Keys))
	for _, key := range reply.Keys {
		if !secret.ValidName(key.Name) || seen[key.Name] || (key.Scope != "user" && key.Scope != "project" && key.Scope != "organization") {
			return nil, errors.New("invalid secret list response")
		}
		seen[key.Name] = true
	}
	slices.SortFunc(reply.Keys, func(a, b SecretKey) int { return strings.Compare(a.Name, b.Name) })
	return reply.Keys, nil
}

// ReadLocalSecret requests one secret through the workspace's local agent.
func ReadLocalSecret(ctx context.Context, socket, name string) (string, error) {
	if !secret.ValidName(name) {
		return "", secret.ErrInvalidKey
	}
	return readLocalCredential(ctx, socket, "/"+name, name, "")
}

// ReadLocalGitToken requests the owner token for the workspace repository.
func ReadLocalGitToken(ctx context.Context, socket, repository string) (string, error) {
	if !ValidGitRepository(repository) {
		return "", ErrGuestUnauthorized
	}
	return readLocalCredential(ctx, socket, "/git", "git", repository)
}

func readLocalCredential(ctx context.Context, socket, path, name, repository string) (string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://shiftbox.local"+path, nil)
	if err != nil {
		return "", err
	}
	if repository != "" {
		req.Header.Set(GitRepositoryHeader, repository)
	}
	reply, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer reply.Body.Close()
	switch reply.StatusCode {
	case http.StatusOK:
		return decodeSecretReply(reply.Body, name)
	case http.StatusNotFound:
		return "", sql.ErrNoRows
	case http.StatusForbidden:
		return "", ErrGuestUnauthorized
	case http.StatusConflict:
		return "", ErrGitReconnect
	default:
		return "", errors.New("workspace secret service is unavailable")
	}
}

func localGitToken(w http.ResponseWriter, r *http.Request, client *http.Client, pollEndpoint string) {
	headers := r.Header.Values(GitRepositoryHeader)
	if len(headers) != 1 || !ValidGitRepository(headers[0]) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	token, err := requestGitToken(r.Context(), client, pollEndpoint, headers[0])
	switch {
	case errors.Is(err, ErrGuestUnauthorized):
		w.WriteHeader(http.StatusForbidden)
	case errors.Is(err, ErrGitReconnect):
		w.WriteHeader(http.StatusConflict)
	case err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(secretReply{Name: "git", Value: &token})
	}
}

func decodeSecretReply(body io.Reader, name string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(body, secretReplyLimit+1))
	defer clear(raw)
	var reply secretReply
	if err != nil || len(raw) > secretReplyLimit || json.Unmarshal(raw, &reply) != nil ||
		reply.Name != name || reply.Value == nil {
		return "", errors.New("invalid secret response")
	}
	return *reply.Value, nil
}
