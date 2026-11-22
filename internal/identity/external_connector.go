package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	api "github.com/dexidp/dex/api/v2"
	"google.golang.org/grpc"
)

// ErrExternalConnector hides upstream and Dex errors that can contain credentials.
var ErrExternalConnector = errors.New("external identity connector is unavailable")

var connectorIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ExternalConnectorConfig is the reviewed configuration for one upstream issuer.
type ExternalConnectorConfig struct {
	ID, Issuer, ClientID, ClientSecret string
	RootCAFile                         string
}

// Validate checks the stable binding namespace and configured OAuth client.
func (c ExternalConnectorConfig) Validate() error {
	if !connectorIDPattern.MatchString(c.ID) || c.ID == "local" || !identityURL(c.Issuer) ||
		strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" ||
		(c.RootCAFile != "" && !filepath.IsAbs(c.RootCAFile)) {
		return ErrExternalConnector
	}
	return nil
}

// Discover verifies the upstream's discovery metadata and its HTTPS endpoints.
func (c ExternalConnectorConfig) Discover(ctx context.Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	if _, err := NewOIDC(ctx, c.Issuer, c.ClientID, client, time.Now); err != nil {
		return ErrExternalConnector
	}
	return nil
}

// ExternalConnectorAdmin calls the managed Dex connector API over private mTLS.
type ExternalConnectorAdmin struct {
	Client ExternalConnectorAPI
}

// ExternalConnectorAPI is the connector subset of the private Dex API.
type ExternalConnectorAPI interface {
	CreateConnector(context.Context, *api.CreateConnectorReq, ...grpc.CallOption) (*api.CreateConnectorResp, error)
	DeleteConnector(context.Context, *api.DeleteConnectorReq, ...grpc.CallOption) (*api.DeleteConnectorResp, error)
}

// Create stages a new Dex connector ID. ShiftBox only uses it after activation.
func (a ExternalConnectorAdmin) Create(ctx context.Context, dexID, origin string, cfg ExternalConnectorConfig) error {
	if a.Client == nil || !connectorIDPattern.MatchString(dexID) || !strings.HasPrefix(dexID, "shiftbox-external-") ||
		!identityURL(origin) || cfg.Validate() != nil {
		return ErrExternalConnector
	}
	document := map[string]any{"issuer": cfg.Issuer, "clientID": cfg.ClientID, "clientSecret": cfg.ClientSecret,
		"redirectURI": origin + "/dex/callback", "pkceChallenge": "S256"}
	if cfg.RootCAFile != "" {
		document["rootCAs"] = []string{cfg.RootCAFile}
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return ErrExternalConnector
	}
	defer clear(payload)
	result, err := a.Client.CreateConnector(ctx, &api.CreateConnectorReq{Connector: &api.Connector{
		Id: dexID, Type: "oidc", Name: "Organization", Config: payload, GrantTypes: []string{"authorization_code"},
	}})
	if err != nil || result == nil || result.AlreadyExists {
		return ErrExternalConnector
	}
	return nil
}

// Delete retires a staged Dex connector. Absence is an idempotent success.
func (a ExternalConnectorAdmin) Delete(ctx context.Context, dexID string) error {
	if a.Client == nil || !strings.HasPrefix(dexID, "shiftbox-external-") {
		return ErrExternalConnector
	}
	result, err := a.Client.DeleteConnector(ctx, &api.DeleteConnectorReq{Id: dexID})
	if err != nil || result == nil {
		return ErrExternalConnector
	}
	return nil
}
