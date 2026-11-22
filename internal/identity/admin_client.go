package identity

import (
	"crypto/tls"
	api "github.com/dexidp/dex/api/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewDexAdminClient connects only to the private loopback administration endpoint
// with verified TLS 1.3 and a client certificate. Composition supplies dedicated
// administrator credentials; guest trust must never be used for this connection.
// Calls need bounded contexts; the caller closes the connection on shutdown.
func NewDexAdminClient(address string, config *tls.Config) (api.DexClient, *grpc.ClientConn, error) {
	if !loopbackAddress(address) || config == nil || config.InsecureSkipVerify || config.RootCAs == nil || config.ServerName == "" || len(config.Certificates) == 0 || (config.MaxVersion != 0 && config.MaxVersion < tls.VersionTLS13) {
		return nil, nil, ErrPasswordAdministration
	}
	if len(config.Certificates[0].Certificate) == 0 || config.Certificates[0].PrivateKey == nil {
		return nil, nil, ErrPasswordAdministration
	}
	transport := config.Clone()
	transport.MinVersion = tls.VersionTLS13
	conn, err := grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(credentials.NewTLS(transport)))
	if err != nil {
		return nil, nil, ErrPasswordAdministration
	}
	return api.NewDexClient(conn), conn, nil
}
