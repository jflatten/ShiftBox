package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubTokenURL = "https://github.com/login/oauth/access_token"
	githubAPIURL   = "https://api.github.com"
)

type githubHTTP struct {
	client                 *http.Client
	clientID, clientSecret string
	installationID         int64
	now                    func() time.Time
}

func (h githubHTTP) refresh(ctx context.Context, refreshToken string) (githubTokens, error) {
	return h.exchange(ctx, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	})
}

func (h githubHTTP) exchange(ctx context.Context, form url.Values) (githubTokens, error) {
	form.Set("client_id", h.clientID)
	form.Set("client_secret", h.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return githubTokens{}, errors.New("cannot construct GitHub token exchange")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var result struct {
		Access        string `json:"access_token"`
		Refresh       string `json:"refresh_token"`
		AccessExpiry  int64  `json:"expires_in"`
		RefreshExpiry int64  `json:"refresh_token_expires_in"`
		TokenType     string `json:"token_type"`
		Error         string `json:"error"`
	}
	status, err := h.send(req, &result, 32<<10)
	if err != nil {
		return githubTokens{}, err
	}
	switch result.Error {
	case "bad_refresh_token", "bad_verification_code":
		return githubTokens{}, ErrGitHubReconnect
	case "":
	default:
		return githubTokens{}, errors.New("GitHub rejected the token exchange")
	}
	if status != http.StatusOK || !strings.EqualFold(result.TokenType, "bearer") {
		return githubTokens{}, errors.New("GitHub rejected the token exchange")
	}
	now := h.now()
	pair := githubTokens{Access: result.Access, Refresh: result.Refresh}
	if result.AccessExpiry > 0 && result.AccessExpiry <= int64((24*time.Hour)/time.Second) &&
		result.RefreshExpiry > 0 && result.RefreshExpiry <= int64((366*24*time.Hour)/time.Second) {
		pair.AccessExpiresAt = now.Add(time.Duration(result.AccessExpiry) * time.Second)
		pair.RefreshExpiresAt = now.Add(time.Duration(result.RefreshExpiry) * time.Second)
	} else if result.AccessExpiry != 0 || result.RefreshExpiry != 0 || result.Refresh != "" {
		return githubTokens{}, errors.New("GitHub returned invalid token lifetimes")
	}
	if !pair.currentAt(now) || !pair.usable(now) {
		return githubTokens{}, errors.New("GitHub returned an invalid token pair")
	}
	return pair, nil
}

func (h githubHTTP) covers(ctx context.Context, token, repository string) error {
	repo, err := url.Parse(repository)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	name := strings.TrimSuffix(strings.Trim(repo.Path, "/"), ".git")
	path := "/user/installations/" + strconv.FormatInt(h.installationID, 10) + "/repositories"

	// GitHub's installation-scoped list proves both user and app access.
	// Pages are fetched only from the configured installation on GitHub.com.
	for page := 1; ; page++ {
		var listing struct {
			Count int `json:"total_count"`
			Repos []struct {
				Name string `json:"full_name"`
			} `json:"repositories"`
		}
		status, err := h.get(ctx, token, path+"?per_page=100&page="+strconv.Itoa(page), &listing, 8<<20)
		if err != nil {
			return err
		}
		switch status {
		case http.StatusOK:
		case http.StatusUnauthorized:
			return ErrGitHubReconnect
		case http.StatusNotFound:
			if err := h.checkInstallation(ctx, token); err != nil {
				return err
			}
			return ErrGitHubRepositoryDenied
		default:
			return errors.New("GitHub repository coverage is unavailable")
		}
		for _, item := range listing.Repos {
			if strings.EqualFold(item.Name, name) {
				return nil
			}
		}
		if len(listing.Repos) == 0 || page >= (max(0, listing.Count-1)/100)+1 {
			return ErrGitHubRepositoryDenied
		}
	}
}

// checkInstallation distinguishes a stale user authorization from a valid
// authorization that simply does not cover a particular repository.
func (h githubHTTP) checkInstallation(ctx context.Context, token string) error {
	for page := 1; ; page++ {
		var listing struct {
			Count         *int `json:"total_count"`
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		status, err := h.get(ctx, token, "/user/installations?per_page=100&page="+strconv.Itoa(page), &listing, 8<<20)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized {
			return ErrGitHubReconnect
		}
		if status != http.StatusOK || listing.Count == nil || listing.Installations == nil || *listing.Count < len(listing.Installations) {
			return errors.New("GitHub installation access could not be checked")
		}
		for _, installation := range listing.Installations {
			if installation.ID == h.installationID {
				return nil
			}
		}
		if len(listing.Installations) == 0 || page >= (max(0, *listing.Count-1)/100)+1 {
			return ErrGitHubReconnect
		}
	}
}

// get sends an owner-authorized API request to GitHub's configured host. The
// caller constructs the path from known API routes and escaped components.
func (h githubHTTP) get(ctx context.Context, token, path string, target any, limit int64) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+path, nil)
	if err != nil {
		return 0, errors.New("cannot construct GitHub API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	return h.send(req, target, limit)
}

func (h githubHTTP) send(req *http.Request, target any, limit int64) (int, error) {
	response, err := h.client.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return 0, req.Context().Err()
		}
		return 0, errors.New("GitHub HTTPS request failed")
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status != http.StatusOK && req.Method == http.MethodGet {
		return status, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return status, errors.New("GitHub returned an unreadable response")
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return status, errors.New("GitHub returned an invalid response")
	}
	return status, nil
}
