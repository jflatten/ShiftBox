package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrInvalidIdentity is safe to surface without exposing tokens or provider diagnostics.
var ErrInvalidIdentity = errors.New("identity verification failed")

// ConnectorIdentity comes from controller configuration and the pending login state.
// The issuer is the upstream's exact identity namespace, not an email domain.
type ConnectorIdentity struct {
	ID     string
	Issuer string
}

// VerifiedIdentity carries a stable upstream binding plus non-authoritative profile fields.
type VerifiedIdentity struct {
	Issuer        string
	Subject       string
	ConnectorID   string
	Email         string
	EmailVerified bool
	Name          string
}

// OIDC verifies the product-managed Dex provider without bypassing TLS or JWT checks.
type OIDC struct {
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	clientID string
	now      func() time.Time
}

// NewOIDC discovers the pinned issuer using the controller's configured TLS trust.
func NewOIDC(ctx context.Context, issuer, clientID string, client *http.Client, now func() time.Time) (*OIDC, error) {
	if !identityURL(issuer) || clientID == "" || client == nil || now == nil {
		return nil, ErrInvalidIdentity
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, ErrInvalidIdentity
	}
	var metadata struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, ErrInvalidIdentity
	}
	endpoints := provider.Endpoint()
	if !identityURL(metadata.JWKSURL) || !identityURL(endpoints.AuthURL) || !identityURL(endpoints.TokenURL) {
		return nil, ErrInvalidIdentity
	}
	verifier := provider.VerifierContext(oidc.ClientContext(ctx, client), &oidc.Config{ClientID: clientID, Now: now})
	return &OIDC{verifier: verifier, client: client, clientID: clientID, now: now}, nil
}

// Verify requires a nonce-bound authorization flow and Dex's signed federated:id
// claims. Refreshing a token does not establish a new upstream authorization time.
func (p *OIDC) Verify(ctx context.Context, rawToken, expectedNonce string, connector ConnectorIdentity) (VerifiedIdentity, error) {
	if expectedNonce == "" || connector.ID == "" || !identityURL(connector.Issuer) {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	token, err := p.verifier.Verify(oidc.ClientContext(ctx, p.client), rawToken)
	if err != nil {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	if token.Subject == "" || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(expectedNonce)) != 1 || token.IssuedAt.IsZero() || token.IssuedAt.After(p.now()) || !p.now().Before(token.Expiry) {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	var claims struct {
		Email           string `json:"email"`
		EmailVerified   bool   `json:"email_verified"`
		Name            string `json:"name"`
		AuthorizedParty string `json:"azp"`
		Federated       struct {
			ConnectorID string `json:"connector_id"`
			UserID      string `json:"user_id"`
		} `json:"federated_claims"`
	}
	if err := token.Claims(&claims); err != nil {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	if claims.Federated.ConnectorID != connector.ID || claims.Federated.UserID == "" {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	if (len(token.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != p.clientID {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	return VerifiedIdentity{Issuer: connector.Issuer, Subject: claims.Federated.UserID, ConnectorID: connector.ID, Email: claims.Email, EmailVerified: claims.EmailVerified, Name: claims.Name}, nil
}

func identityURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
