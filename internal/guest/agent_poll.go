package guest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const pollPath = "/internal/v1/guest/poll"

var errAgentOperationPending = errors.New("guest operation execution unavailable")

// Poll authenticates a single poll using the saved guest identity.
func (identity AgentIdentity) Poll(ctx context.Context, endpoint, serverName, protocol, release string) error {
	client := identity.pollClient(serverName)
	defer client.CloseIdleConnections()
	return pollAgent(ctx, client, endpoint, protocol, release)
}

func (identity AgentIdentity) pollClient(serverName string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: identity.Roots,
			Certificates: []tls.Certificate{identity.Certificate}, ServerName: serverName,
		}},
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func pollAgent(ctx context.Context, client *http.Client, endpoint, protocol, release string) error {
	if !validPollEndpoint(endpoint) || protocol == "" || release == "" {
		return errors.New("invalid guest poll configuration")
	}
	var input = struct {
		ProtocolVersion string `json:"protocolVersion"`
		GuestRelease    string `json:"guestRelease"`
	}{protocol, release}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid guest poll request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("guest poll HTTPS exchange failed")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusUnauthorized:
		return ErrGuestUnauthorized
	default:
		return errors.New("controller did not accept guest poll")
	}
	var result struct {
		ProtocolVersion string          `json:"protocolVersion"`
		ServerTime      time.Time       `json:"serverTime"`
		Generation      int64           `json:"workspaceGeneration"`
		DesiredState    string          `json:"desiredState"`
		Operation       json.RawMessage `json:"operation"`
		Certificate     struct {
			RenewAfter time.Time `json:"renewAfter"`
		} `json:"certificate"`
		NextPollSeconds int `json:"nextPollAfterSeconds"`
	}
	if err := decodeAgentJSON(response.Body, &result); err != nil || result.ProtocolVersion != protocol ||
		result.Generation < 1 || result.ServerTime.IsZero() || result.Certificate.RenewAfter.IsZero() {
		return errors.New("invalid guest poll response")
	}
	if len(result.Operation) > 0 && string(result.Operation) != "null" {
		return errAgentOperationPending
	}
	return nil
}

func validPollEndpoint(endpoint string) bool {
	target, err := url.Parse(endpoint)
	return err == nil && target.Scheme == "https" && target.Hostname() != "" && target.Path == pollPath &&
		target.User == nil && target.RawQuery == "" && !target.ForceQuery && target.Fragment == ""
}

// ServeAgent loads the on-disk identity or enrolls from mounted recovery media,
// then maintains authenticated contact until stopped by the supervisor.
func ServeAgent(ctx context.Context, stateDir, seedPath, endpoint, protocol, release string) error {
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(seedPath) || !validPollEndpoint(endpoint) {
		return errors.New("invalid guest service configuration")
	}
	identity, err := LoadAgentIdentity(stateDir, time.Now())
	if err != nil {
		seed, err := os.Open(seedPath)
		if err != nil {
			return errors.New("no valid guest identity or recovery media")
		}
		identity, err = EnrollAgent(ctx, stateDir, seed, protocol, release, time.Now())
		seed.Close()
		if err != nil {
			return err
		}
	}
	client := identity.pollClient("")
	defer client.CloseIdleConnections()
	for {
		if err := pollAgent(ctx, client, endpoint, protocol, release); err != nil {
			return err
		}
		delay := 27*time.Second + rand.N(6*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
