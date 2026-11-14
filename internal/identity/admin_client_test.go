package identity_test

import (
	"crypto/tls"
	"crypto/x509"
	"github.com/jflatten/ShiftBox/internal/identity"
	"testing"
)

// TestAdminClientRejectsInsecureTransport prevents password management over public, plaintext, or unverified connections.
func TestAdminClientRejectsInsecureTransport(t *testing.T) {
	for _, tc := range []struct {
		address string
		config  *tls.Config
	}{
		{"127.0.0.1:5557", nil},
		{"192.0.2.1:5557", &tls.Config{RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{}}, ServerName: "admin.test"}},
		{"127.0.0.1:5557", &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "admin.test"}},
		{"127.0.0.1:5557", &tls.Config{Certificates: []tls.Certificate{{}}, ServerName: "admin.test"}},
		{"127.0.0.1:5557", &tls.Config{RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{}}, ServerName: "admin.test", InsecureSkipVerify: true}},
	} {
		if _, conn, err := identity.NewDexAdminClient(tc.address, tc.config); err == nil {
			conn.Close()
			t.Errorf("NewDexAdminClient(%s) accepted unsafe transport", tc.address)
		}
	}
}
