package guest

import "crypto/tls"

// ServerTLS requires verified guest certificates on the separate internal listener.
// Session tickets stay disabled so new connections always prove current possession.
func ServerTLS(certificate tls.Certificate, authority *Authority) *tls.Config {
	return &tls.Config{
		MinVersion:             tls.VersionTLS13,
		Certificates:           []tls.Certificate{certificate},
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              authority.roots(),
		SessionTicketsDisabled: true,
	}
}

// ClientTLS verifies the installation authority and exact internal server name.
func ClientTLS(certificate tls.Certificate, authority *Authority, serverName string) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      authority.roots(),
		ServerName:   serverName,
	}
}
