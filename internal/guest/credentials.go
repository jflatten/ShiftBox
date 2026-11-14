package guest

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// GitAccessTokenPath is served only on the authenticated guest listener.
const GitAccessTokenPath = "/internal/v1/guest/git/github:access-token"

func guestGitAccessToken(ctx context.Context, client *http.Client, pollEndpoint string) (string, error) {
	if !validPollEndpoint(pollEndpoint) {
		return "", errors.New("invalid guest credential endpoint")
	}
	endpoint, err := url.Parse(pollEndpoint)
	if err != nil {
		return "", errors.New("invalid guest credential endpoint")
	}
	endpoint.Path = GitAccessTokenPath
	endpoint.RawPath = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return "", errors.New("cannot construct guest Git credential request")
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("guest Git credential exchange failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("owner Git authorization is unavailable")
	}
	var result struct {
		AccessToken string `json:"accessToken"`
	}
	if err := decodeAgentJSON(response.Body, &result); err != nil || !ValidGitAccessToken(result.AccessToken) {
		return "", errors.New("controller returned an invalid guest Git credential")
	}
	return result.AccessToken, nil
}

// ValidGitAccessToken checks the format and size before delivering or using a token.
func ValidGitAccessToken(token string) bool {
	return token != "" && len(token) <= 4096 && !strings.ContainsAny(token, " \r\n\t\x00")
}
