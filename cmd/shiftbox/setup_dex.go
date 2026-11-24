package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

type dexMode uint8

const (
	localSetupDex dexMode = iota
	forwardedSetupDex
	privateSetupDex
	controllerDex
)

func startSetupDex(ctx context.Context, dir, binary, origin string, server tls.Certificate, postgres *identity.DexPostgres) (identity.PasswordAdmin, *app.SetupLogin, <-chan struct{}, func(), error) {
	admin, login, _, done, cleanup, err := startDex(ctx, dir, binary, origin, server, postgres, localSetupDex)
	return admin, login, done, cleanup, err
}

func startControllerDex(ctx context.Context, dir, binary, origin string, postgres *identity.DexPostgres) (identity.PasswordAdmin, *app.SetupLogin, http.Handler, <-chan struct{}, func(), error) {
	if postgres == nil {
		info, err := os.Lstat(filepath.Join(dir, "dex.db"))
		if err != nil || !info.Mode().IsRegular() {
			return identity.PasswordAdmin{}, nil, nil, nil, nil, errors.New("retained local identity database is unavailable")
		}
	}
	return startDex(ctx, dir, binary, origin, tls.Certificate{}, postgres, controllerDex)
}

func startDex(ctx context.Context, dir, binary, origin string, server tls.Certificate, postgres *identity.DexPostgres, mode dexMode) (identity.PasswordAdmin, *app.SetupLogin, http.Handler, <-chan struct{}, func(), error) {
	var admin identity.PasswordAdmin
	runtime, err := os.MkdirTemp(dir, ".dex-runtime-")
	if err != nil {
		return admin, nil, nil, nil, nil, errors.New("cannot stage local Dex configuration")
	}
	var cleanup []func()
	cleanup = append(cleanup, func() { os.RemoveAll(runtime) })
	closeAll := func() {
		for i := len(cleanup) - 1; i >= 0; i-- {
			cleanup[i]()
		}
	}
	success := false
	defer func() {
		if !success {
			closeAll()
		}
	}()

	if mode == controllerDex || mode == privateSetupDex {
		server, err = app.EnsureBootstrapCertificate(filepath.Join(runtime, "loopback-tls.pem"), time.Now())
		if err != nil {
			return admin, nil, nil, nil, nil, errors.New("cannot generate managed Dex loopback TLS")
		}
	}

	addresses := make([]string, 3)
	reservations := make([]net.Listener, len(addresses))
	for i := range reservations {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return admin, nil, nil, nil, nil, errors.New("cannot reserve local Dex ports")
		}
		reservations[i] = listener
		addresses[i] = listener.Addr().String()
		cleanup = append(cleanup, func() { listener.Close() })
	}
	clientCert, err := setupDexClientCertificate()
	if err != nil {
		return admin, nil, nil, nil, nil, errors.New("cannot create local Dex administrator certificate")
	}
	serverKey, err := x509.MarshalPKCS8PrivateKey(server.PrivateKey)
	if err != nil {
		return admin, nil, nil, nil, nil, errors.New("cannot encode local Dex TLS material")
	}
	serverFile := filepath.Join(runtime, "server.pem")
	keyFile := filepath.Join(runtime, "server.key")
	clientCA := filepath.Join(runtime, "client-ca.pem")
	for _, file := range []struct {
		path, kind string
		der        []byte
	}{
		{serverFile, "CERTIFICATE", server.Certificate[0]},
		{keyFile, "PRIVATE KEY", serverKey},
		{clientCA, "CERTIFICATE", clientCert.Certificate[0]},
	} {
		if err := os.WriteFile(file.path, pem.EncodeToMemory(&pem.Block{Type: file.kind, Bytes: file.der}), 0600); err != nil {
			return admin, nil, nil, nil, nil, errors.New("cannot stage local Dex TLS material")
		}
	}
	var secret [32]byte
	rand.Read(secret[:])
	webOrigin := "https://" + addresses[0]
	issuer := webOrigin + "/dex"
	if mode != localSetupDex {
		issuer = origin + "/dex"
	}
	cfg := identity.DexConfig{
		Issuer: issuer, RedirectURI: origin + "/auth/callback",
		WebAddress: addresses[0], TelemetryAddress: addresses[1], GRPCAddress: addresses[2],
		TLSCertFile: serverFile, TLSKeyFile: keyFile, GRPCCertFile: serverFile, GRPCKeyFile: keyFile, GRPCClientCAFile: clientCA,
		SQLitePath: filepath.Join(dir, "dex.db"), ClientID: "shiftbox-local", ClientSecret: hex.EncodeToString(secret[:]),
	}
	if postgres != nil {
		cfg.SQLitePath = ""
		cfg.Postgres = postgres
	}
	frontend, err := loginFrontendAssets()
	if err != nil {
		return admin, nil, nil, nil, nil, errors.New("cannot load the login frontend")
	}
	if frontend != nil {
		cfg.FrontendDir = filepath.Join(runtime, "frontend")
		if err := os.CopyFS(cfg.FrontendDir, frontend); err != nil {
			return admin, nil, nil, nil, nil, errors.New("cannot stage the login frontend")
		}
	}
	configPath := filepath.Join(runtime, "dex.json")
	if err := identity.WriteDexConfig(configPath, cfg); err != nil {
		return admin, nil, nil, nil, nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Leaf)
	client, connection, err := identity.NewDexAdminClient(cfg.GRPCAddress, &tls.Config{
		RootCAs: roots, ServerName: "127.0.0.1", Certificates: []tls.Certificate{clientCert},
	})
	if err != nil {
		return admin, nil, nil, nil, nil, err
	}
	cleanup = append(cleanup, func() { connection.Close() })
	for _, listener := range reservations {
		listener.Close()
	}
	transport := &http.Transport{}
	cleanup = append(cleanup, transport.CloseIdleConnections)
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: configPath,
		HealthURL: "http://" + cfg.TelemetryAddress + "/healthz/ready",
		Client:    &http.Client{Transport: transport, Timeout: time.Second}, PollInterval: 50 * time.Millisecond,
		ManageExternalConnectors: mode == controllerDex}
	dexCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = process.Run(dexCtx)
	}()
	cleanup = append(cleanup, func() { cancel(); <-done })
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	startup, stop := context.WithTimeout(ctx, 12*time.Second)
	defer stop()
	for !process.Healthy() {
		select {
		case <-done:
			return admin, nil, nil, nil, nil, errors.New("local Dex failed to start; check the installed Dex version")
		case <-startup.Done():
			return admin, nil, nil, nil, nil, errors.New("local Dex did not become ready")
		case <-ticker.C:
		}
	}
	browserTransport := &http.Transport{
		TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	cleanup = append(cleanup, browserTransport.CloseIdleConnections)
	var dexWeb http.Handler
	clientTransport := http.RoundTripper(browserTransport)
	if mode != localSetupDex {
		public, err := url.Parse(issuer)
		if err != nil {
			return admin, nil, nil, nil, nil, errors.New("invalid management identity origin")
		}
		private := &url.URL{Scheme: "https", Host: addresses[0]}
		clientTransport = dexBackchannel{issuer: public, target: private, transport: browserTransport}
		dexWeb = dexBrowserHandler(public, private, browserTransport)
	}
	oidcClient := &http.Client{Transport: clientTransport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider, err := identity.NewOIDC(startup, cfg.Issuer, cfg.ClientID, oidcClient, time.Now)
	if err != nil {
		return admin, nil, nil, nil, nil, errors.New("cannot discover local Dex identity provider")
	}
	login := &app.SetupLogin{Provider: provider, Config: provider.LocalLogin(cfg.ClientSecret, cfg.RedirectURI), Issuer: cfg.Issuer}
	success = true
	return identity.PasswordAdmin{Client: client}, login, dexWeb, done, closeAll, nil
}

// dexBackchannel keeps discovery, token exchange and signing-key retrieval on
// the private TLS listener while validating the browser-facing issuer.
type dexBackchannel struct {
	issuer, target *url.URL
	transport      http.RoundTripper
}

// RoundTrip routes requests within the configured issuer to the private Dex listener.
func (d dexBackchannel) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != d.issuer.Scheme || r.URL.Host != d.issuer.Host ||
		!strings.HasPrefix(r.URL.Path, d.issuer.Path+"/") {
		return nil, errors.New("identity endpoint is outside the configured issuer")
	}
	out := r.Clone(r.Context())
	out.URL.Host = d.target.Host
	return d.transport.RoundTrip(out)
}

func dexBrowserHandler(issuer, target *url.URL, transport http.RoundTripper) http.Handler {
	upstream := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			proxy.Sanitize(req.Out)
			req.Out.Header.Del("Authorization")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || r.Host != issuer.Host {
			w.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		// Only the interactive login and its assets are browser-facing.
		// The controller exchanges codes and reads keys over private TLS.
		path := strings.TrimPrefix(r.URL.Path, issuer.Path)
		switch {
		case path == "/auth", strings.HasPrefix(path, "/auth/"),
			path == "/approval", path == "/callback", strings.HasPrefix(path, "/callback/"),
			strings.HasPrefix(path, "/static/"), strings.HasPrefix(path, "/theme/"):
		default:
			http.NotFound(w, r)
			return
		}
		upstream.ServeHTTP(w, r)
	})
}

func setupDexClientCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
