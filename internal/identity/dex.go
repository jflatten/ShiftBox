package identity

import (
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
)

// DexPostgres contains scoped identity-store credentials. Schema/search-path
// ownership is verified during profile activation; Dex has no schema option.
type DexPostgres struct {
	Host     string
	Port     uint16
	Database string
	User     string
	Password string
	CAFile   string
}

// DexConfig describes the private managed identity service, with separate admin
// client trust that must not authorize workspace guest certificates.
type DexConfig struct {
	Issuer           string
	WebAddress       string
	TLSCertFile      string
	TLSKeyFile       string
	TelemetryAddress string
	GRPCAddress      string
	GRPCCertFile     string
	GRPCKeyFile      string
	GRPCClientCAFile string
	SQLitePath       string
	Postgres         *DexPostgres
	ClientID         string
	ClientSecret     string
	RedirectURI      string
}

// WriteDexConfig atomically replaces a private JSON configuration, accepted by
// Dex's YAML reader. Passwords and client secrets never enter process arguments.
func WriteDexConfig(path string, cfg DexConfig) error {
	if !filepath.IsAbs(path) || !identityURL(cfg.Issuer) || !identityURL(cfg.RedirectURI) || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return errors.New("invalid managed identity configuration")
	}
	for _, address := range []string{cfg.WebAddress, cfg.TelemetryAddress, cfg.GRPCAddress} {
		if !loopbackAddress(address) {
			return errors.New("identity listeners must use explicit loopback addresses")
		}
	}
	for _, file := range []string{cfg.TLSCertFile, cfg.TLSKeyFile, cfg.GRPCCertFile, cfg.GRPCKeyFile, cfg.GRPCClientCAFile} {
		if !filepath.IsAbs(file) {
			return errors.New("identity TLS material requires explicit absolute paths")
		}
	}
	var storage map[string]any
	switch {
	case cfg.Postgres == nil && filepath.IsAbs(cfg.SQLitePath):
		storage = map[string]any{"type": "sqlite3", "config": map[string]any{"file": cfg.SQLitePath}}
	case cfg.Postgres != nil && cfg.SQLitePath == "":
		p := cfg.Postgres
		if p.Host == "" || p.Port == 0 || p.Database == "" || p.User == "" || p.Password == "" || (p.CAFile != "" && !filepath.IsAbs(p.CAFile)) {
			return errors.New("invalid scoped identity database configuration")
		}
		storage = map[string]any{"type": "postgres", "config": map[string]any{"host": p.Host, "port": p.Port, "database": p.Database, "user": p.User, "password": p.Password, "ssl": map[string]any{"mode": "verify-full", "caFile": p.CAFile}}}
	default:
		return errors.New("choose exactly one explicit identity data profile")
	}
	document := map[string]any{
		"issuer": cfg.Issuer, "storage": storage, "enablePasswordDB": true,
		"web":           map[string]any{"https": cfg.WebAddress, "tlsCert": cfg.TLSCertFile, "tlsKey": cfg.TLSKeyFile, "tlsMinVersion": "1.3"},
		"grpc":          map[string]any{"addr": cfg.GRPCAddress, "tlsCert": cfg.GRPCCertFile, "tlsKey": cfg.GRPCKeyFile, "tlsClientCA": cfg.GRPCClientCAFile, "tlsMinVersion": "1.3"},
		"telemetry":     map[string]any{"http": cfg.TelemetryAddress},
		"logger":        map[string]any{"level": "error", "format": "json", "excludeFields": []string{"email", "username", "preferred_username", "groups"}},
		"oauth2":        map[string]any{"responseTypes": []string{"code"}, "grantTypes": []string{"authorization_code", "refresh_token"}, "pkce": map[string]any{"enforce": true, "codeChallengeMethodsSupported": []string{"S256"}}},
		"staticClients": []any{map[string]any{"id": cfg.ClientID, "secret": cfg.ClientSecret, "name": "ShiftBox", "redirectURIs": []string{cfg.RedirectURI}}},
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return errors.New("cannot encode identity configuration")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".dex-config-*")
	if err != nil {
		return errors.New("cannot stage identity configuration")
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return errors.New("cannot write identity configuration")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return errors.New("cannot sync identity configuration")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close identity configuration")
	}
	if err := os.Rename(name, path); err != nil {
		return errors.New("cannot activate identity configuration")
	}
	return nil
}

func loopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return false
	}
	value, err := strconv.ParseUint(port, 10, 16)
	return err == nil && value > 0
}
