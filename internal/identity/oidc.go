package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrInvalidIdentity is safe to surface without exposing tokens or provider diagnostics.
var ErrInvalidIdentity = errors.New("identity verification failed")

// ConnectorIdentity comes from controller configuration and the pending login state.
// The issuer is the upstream's exact identity namespace, not an email domain.
type ConnectorIdentity struct {
	ID     string
	Issuer string
	DexID  string
}

// BrokerID is the concrete Dex connector for this stable identity namespace.
func (c ConnectorIdentity) BrokerID() string {
	if c.DexID != "" {
		return c.DexID
	}
	return c.ID
}

// VerifiedIdentity carries a stable upstream binding plus non-authoritative profile fields.
type VerifiedIdentity struct {
	Issuer         string
	Subject        string
	ConnectorID    string
	Email          string
	EmailVerified  bool
	Name           string
	AuthorizedAt   time.Time
	DexConnectorID string
}

// OIDC verifies the product-managed Dex provider without bypassing TLS or JWT checks.
type OIDC struct {
	verifier *oidc.IDTokenVerifier
	endpoint oauth2.Endpoint
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
	return &OIDC{verifier: verifier, endpoint: endpoints, client: client, clientID: clientID, now: now}, nil
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
		AuthTime        int64  `json:"auth_time"`
		Federated       struct {
			ConnectorID string `json:"connector_id"`
			UserID      string `json:"user_id"`
		} `json:"federated_claims"`
	}
	if err := token.Claims(&claims); err != nil {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	if claims.Federated.ConnectorID != connector.BrokerID() || claims.Federated.UserID == "" {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	if (len(token.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != p.clientID {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	verified := VerifiedIdentity{Issuer: connector.Issuer, Subject: claims.Federated.UserID, ConnectorID: connector.ID,
		Email: claims.Email, EmailVerified: claims.EmailVerified, Name: claims.Name, DexConnectorID: connector.DexID}
	if claims.AuthTime > 0 {
		verified.AuthorizedAt = time.Unix(claims.AuthTime, 0).UTC()
	}
	return verified, nil
}

// VerifyExternal requires Dex's signed authentication time from the current
// authorization attempt. A freshly minted token alone carries no such proof.
func (p *OIDC) VerifyExternal(ctx context.Context, rawToken, nonce string, connector ConnectorIdentity, startedAt time.Time) (VerifiedIdentity, error) {
	verified, err := p.Verify(ctx, rawToken, nonce, connector)
	if err != nil || startedAt.IsZero() || verified.AuthorizedAt.IsZero() ||
		verified.AuthorizedAt.Before(startedAt.Truncate(time.Second)) || verified.AuthorizedAt.After(p.now()) {
		return VerifiedIdentity{}, ErrInvalidIdentity
	}
	return verified, nil
}

func identityURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// LocalLogin configures the browser authorization-code flow for the private Dex connector.
func (p *OIDC) LocalLogin(secret, redirect string) oauth2.Config {
	return oauth2.Config{ClientID: p.clientID, ClientSecret: secret, RedirectURL: redirect,
		Endpoint: p.endpoint, Scopes: []string{oidc.ScopeOpenID, "federated:id"}}
}

// ExchangeLocal exchanges a PKCE-bound code and verifies the signed local identity.
func (p *OIDC) ExchangeLocal(ctx context.Context, cfg oauth2.Config, code, verifier, nonce, issuer string) (VerifiedIdentity, error) {
	raw, err := p.exchange(ctx, cfg, code, verifier)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	return p.Verify(ctx, raw, nonce, ConnectorIdentity{ID: "local", Issuer: issuer})
}

// ExchangeExternal accepts only a fresh authorization code for the selected
// upstream connector. Refresh tokens do not enter this flow.
func (p *OIDC) ExchangeExternal(ctx context.Context, cfg oauth2.Config, code, verifier, nonce string, connector ConnectorIdentity, startedAt time.Time) (VerifiedIdentity, error) {
	raw, err := p.exchange(ctx, cfg, code, verifier)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	return p.VerifyExternal(ctx, raw, nonce, connector, startedAt)
}

func (p *OIDC) exchange(ctx context.Context, cfg oauth2.Config, code, verifier string) (string, error) {
	token, err := cfg.Exchange(oidc.ClientContext(ctx, p.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", ErrInvalidIdentity
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return "", ErrInvalidIdentity
	}
	return raw, nil
}
