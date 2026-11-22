package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jflatten/ShiftBox/internal/template"
)

// GitAccessTokenPath is served only on the authenticated guest listener.
const GitAccessTokenPath = "/internal/v1/guest/git/github:access-token"

// GitRepositoryHeader carries an optional expected repository, never a scope selector.
const GitRepositoryHeader = "X-ShiftBox-Git-Repository"

// ErrGitReconnect indicates that the owner must renew GitHub consent.
var ErrGitReconnect = errors.New("GitHub authorization requires reconnection")

func guestGitAccessToken(ctx context.Context, client *http.Client, pollEndpoint string) (string, error) {
	return requestGitToken(ctx, client, pollEndpoint, "")
}

func requestGitToken(ctx context.Context, client *http.Client, pollEndpoint, repository string) (string, error) {
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
	if repository != "" {
		request.Header.Set(GitRepositoryHeader, repository)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("guest Git credential exchange failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return "", ErrGuestUnauthorized
	}
	if response.StatusCode == http.StatusConflict {
		var problem struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&problem); err == nil && problem.Code == "git.reconnect_required" {
			return "", ErrGitReconnect
		}
	}
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

// SameGitRepository compares the HTTPS GitHub repository paths, allowing Git's
// optional .git suffix. Both inputs must identify a valid repository.
func SameGitRepository(a, b string) bool {
	first, second := gitRepositoryPath(a), gitRepositoryPath(b)
	return first != "" && strings.EqualFold(first, second)
}

// ValidGitRepository checks an HTTPS GitHub repository used by the helper.
func ValidGitRepository(repository string) bool { return gitRepositoryPath(repository) != "" }

func gitRepositoryPath(repository string) string {
	if len(repository) > 2048 || !template.ValidSource(repository, "devcontainer.json") {
		return ""
	}
	url, err := url.Parse(repository)
	if err != nil {
		return ""
	}
	path := strings.Trim(url.Path, "/")
	owner, repo, _ := strings.Cut(path, "/")
	for _, part := range []string{owner, repo} {
		if part == "" || strings.ContainsFunc(part, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.')
		}) {
			return ""
		}
	}
	repo = strings.TrimSuffix(strings.ToLower(repo), ".git")
	if repo == "" || repo == "." || repo == ".." {
		return ""
	}
	return owner + "/" + repo
}
