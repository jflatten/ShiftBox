package identity

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	api "github.com/dexidp/dex/api/v2"
	"google.golang.org/grpc"
)

type externalConnectorFixture struct {
	created *api.Connector
	fail    bool
}

func (f *externalConnectorFixture) CreateConnector(_ context.Context, request *api.CreateConnectorReq, _ ...grpc.CallOption) (*api.CreateConnectorResp, error) {
	f.created = request.Connector
	f.created.Config = append([]byte(nil), f.created.Config...)
	if f.fail {
		return nil, errors.New("private-fixture-secret")
	}
	return &api.CreateConnectorResp{}, nil
}

func (f *externalConnectorFixture) DeleteConnector(context.Context, *api.DeleteConnectorReq, ...grpc.CallOption) (*api.DeleteConnectorResp, error) {
	return &api.DeleteConnectorResp{}, nil
}

// TestExternalDexConnector checks the selected grant type, trusted upstream
// issuer, callback URL, and redaction when private Dex administration fails.
func TestExternalDexConnector(t *testing.T) {
	client := &externalConnectorFixture{}
	dex := ExternalConnectorAdmin{Client: client}
	const origin = "https://management.test"
	cfg := ExternalConnectorConfig{ID: "company", Issuer: "https://upstream.test", ClientID: "fixture", ClientSecret: "private-fixture-secret"}
	if err := dex.Create(t.Context(), "shiftbox-external-fixture", origin, cfg); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(client.created.Config, &stored); err != nil {
		t.Fatal(err)
	}
	if client.created.Type != "oidc" || len(client.created.GrantTypes) != 1 || client.created.GrantTypes[0] != "authorization_code" ||
		stored["redirectURI"] != origin+"/dex/callback" || stored["issuer"] != cfg.Issuer || stored["clientID"] != cfg.ClientID {
		t.Fatal("the created Dex connector does not match the external authorization contract")
	}
	client.fail = true
	if err := dex.Create(t.Context(), "shiftbox-external-fixture", origin, cfg); !errors.Is(err, ErrExternalConnector) {
		t.Fatal("Dex administration failure was not returned as a redacted error")
	}
	cfg.ID = "local"
	if err := cfg.Validate(); err == nil {
		t.Fatal("external configuration accepted the local connector ID")
	}
}
