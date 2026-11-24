package app

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const githubAppAPI = "https://api.github.com"

var (
	githubOwnerName   = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)
	githubAppSlug     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
	githubAppCode     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)
	githubClientPart  = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,256}$`)
	githubPermissions = map[string]string{
		"contents": "write", "pull_requests": "write", "workflows": "write",
		"metadata": "read", "checks": "read", "statuses": "read",
	}
	errGitHubAppProvider = errors.New("GitHub did not return the expected application or installation")
)

type githubAppOwner struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubAppRegistration struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	Slug         string         `json:"slug"`
	ClientID     string         `json:"client_id"`
	ClientSecret string         `json:"client_secret"`
	PrivateKey   string         `json:"pem"`
	Owner        githubAppOwner `json:"owner"`
}

type githubAppInstallation struct {
	ID                  int64             `json:"id"`
	AppID               int64             `json:"app_id"`
	Account             githubAppOwner    `json:"account"`
	SuspendedAt         *time.Time        `json:"suspended_at"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
}

// request does not return transport URLs or provider bodies in errors. Both
// can contain one-use codes or credentials.
func (s *githubAppSetup) request(ctx context.Context, method, path, token string, target any) error {
	r, err := http.NewRequestWithContext(ctx, method, githubAppAPI+path, nil)
	if err != nil {
		return errGitHubAppProvider
	}
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := s.client.Do(r)
	if err != nil {
		return errGitHubAppProvider
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return errGitHubAppProvider
	}
	const limit = 256 << 10
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	defer clear(raw)
	if err != nil || len(raw) > limit || json.Unmarshal(raw, target) != nil {
		return errGitHubAppProvider
	}
	return nil
}

func (s *githubAppSetup) convert(ctx context.Context, code, organization string) (*githubAppRegistration, error) {
	var response struct {
		githubAppRegistration
		ExternalURL string            `json:"external_url"`
		Public      *bool             `json:"public"`
		Permissions map[string]string `json:"permissions"`
		Events      []string          `json:"events"`
	}
	if err := s.request(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", &response); err != nil {
		return nil, err
	}
	app := response.githubAppRegistration
	if app.ID < 1 || app.Owner.ID < 1 || !githubOwnerName.MatchString(app.Owner.Login) ||
		!githubAppSlug.MatchString(app.Slug) || !githubClientPart.MatchString(app.ClientID) || !githubClientPart.MatchString(app.ClientSecret) ||
		!maps.Equal(response.Permissions, githubPermissions) || len(response.Events) != 0 ||
		strings.TrimSuffix(response.ExternalURL, "/") != s.origin || (response.Public != nil && *response.Public) {
		return nil, errGitHubAppProvider
	}
	if organization == "" && app.Owner.Type != "User" ||
		organization != "" && (app.Owner.Type != "Organization" || !strings.EqualFold(organization, app.Owner.Login)) {
		return nil, errGitHubAppProvider
	}
	if _, err := githubAppSigningKey(app.PrivateKey); err != nil {
		return nil, err
	}
	if strings.TrimSpace(app.Name) == "" {
		app.Name = app.Slug
	}
	return &app, nil
}

func githubAppSigningKey(encoded string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(encoded))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errGitHubAppProvider
	}
	defer clear(block.Bytes)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = pkcs8.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errGitHubAppProvider
	}
	return key, nil
}

func (s *githubAppSetup) findInstallation(ctx context.Context, app githubAppRegistration) (*githubAppInstallation, error) {
	key, err := githubAppSigningKey(app.PrivateKey)
	if err != nil {
		return nil, err
	}
	now := s.now()
	claims, err := json.Marshal(struct {
		Issuer    string `json:"iss"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}{app.ClientID, now.Add(-time.Minute).Unix(), now.Add(5 * time.Minute).Unix()})
	if err != nil {
		return nil, errGitHubAppProvider
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return nil, errGitHubAppProvider
	}
	token := input + "." + base64.RawURLEncoding.EncodeToString(signature)
	// A private app can be installed only on its own account. The only
	// accepted installation is the one returned for that account by GitHub.
	var installations []githubAppInstallation
	if err := s.request(ctx, http.MethodGet, "/app/installations", token, &installations); err != nil {
		return nil, err
	}
	if len(installations) != 1 {
		return nil, nil
	}
	install := &installations[0]
	if install.ID <= 0 || install.AppID != app.ID || install.Account.ID != app.Owner.ID ||
		install.Account.Type != app.Owner.Type || install.SuspendedAt != nil ||
		!maps.Equal(install.Permissions, githubPermissions) ||
		(install.RepositorySelection != "selected" && install.RepositorySelection != "all") {
		return nil, nil
	}
	return install, nil
}

func (a githubAppRegistration) installURL() string {
	return "https://github.com/apps/" + a.Slug + "/installations/new"
}

func (a githubAppRegistration) manageURL(id int64) string {
	path := "/settings/installations/" + strconv.FormatInt(id, 10)
	if a.Owner.Type == "Organization" {
		path = "/organizations/" + a.Owner.Login + path
	}
	return "https://github.com" + path
}
