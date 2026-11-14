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
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/identity"
)

func startSetupDex(ctx context.Context, dir, binary, origin string, server tls.Certificate) (identity.PasswordAdmin, *app.SetupLogin, <-chan struct{}, func(), error) {
	var admin identity.PasswordAdmin
	runtime, err := os.MkdirTemp(dir, ".setup-dex-")
	if err != nil {
		return admin, nil, nil, nil, errors.New("cannot stage local Dex configuration")
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

	addresses := make([]string, 3)
	reservations := make([]net.Listener, len(addresses))
	for i := range reservations {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return admin, nil, nil, nil, errors.New("cannot reserve local Dex ports")
		}
		reservations[i] = listener
		addresses[i] = listener.Addr().String()
		cleanup = append(cleanup, func() { listener.Close() })
	}
	clientCert, err := setupDexClientCertificate()
	if err != nil {
		return admin, nil, nil, nil, errors.New("cannot create local Dex administrator certificate")
	}
	serverKey, err := x509.MarshalPKCS8PrivateKey(server.PrivateKey)
	if err != nil {
		return admin, nil, nil, nil, errors.New("cannot encode local Dex TLS material")
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
			return admin, nil, nil, nil, errors.New("cannot stage local Dex TLS material")
		}
	}
	var secret [32]byte
	rand.Read(secret[:])
	webOrigin := "https://" + addresses[0]
	cfg := identity.DexConfig{
		Issuer: webOrigin + "/dex", RedirectURI: origin + "/auth/callback",
		WebAddress: addresses[0], TelemetryAddress: addresses[1], GRPCAddress: addresses[2],
		TLSCertFile: serverFile, TLSKeyFile: keyFile, GRPCCertFile: serverFile, GRPCKeyFile: keyFile, GRPCClientCAFile: clientCA,
		SQLitePath: filepath.Join(dir, "dex.db"), ClientID: "shiftbox-local", ClientSecret: hex.EncodeToString(secret[:]),
	}
	configPath := filepath.Join(runtime, "dex.json")
	if err := identity.WriteDexConfig(configPath, cfg); err != nil {
		return admin, nil, nil, nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Leaf)
	client, connection, err := identity.NewDexAdminClient(cfg.GRPCAddress, &tls.Config{
		RootCAs: roots, ServerName: "127.0.0.1", Certificates: []tls.Certificate{clientCert},
	})
	if err != nil {
		return admin, nil, nil, nil, err
	}
	cleanup = append(cleanup, func() { connection.Close() })
	for _, listener := range reservations {
		listener.Close()
	}
	transport := &http.Transport{}
	cleanup = append(cleanup, transport.CloseIdleConnections)
	process := identity.DexProcess{BinaryPath: binary, ConfigPath: configPath,
		HealthURL: "http://" + cfg.TelemetryAddress + "/healthz/ready",
		Client:    &http.Client{Transport: transport, Timeout: time.Second}, PollInterval: 50 * time.Millisecond}
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
			return admin, nil, nil, nil, errors.New("local Dex failed to start; check the installed Dex version")
		case <-startup.Done():
			return admin, nil, nil, nil, errors.New("local Dex did not become ready")
		case <-ticker.C:
		}
	}
	browserTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	cleanup = append(cleanup, browserTransport.CloseIdleConnections)
	provider, err := identity.NewOIDC(startup, cfg.Issuer, cfg.ClientID, &http.Client{Transport: browserTransport, Timeout: 5 * time.Second}, time.Now)
	if err != nil {
		return admin, nil, nil, nil, errors.New("cannot discover local Dex identity provider")
	}
	login := &app.SetupLogin{Provider: provider, Config: provider.LocalLogin(cfg.ClientSecret, cfg.RedirectURI), Issuer: cfg.Issuer}
	success = true
	return identity.PasswordAdmin{Client: client}, login, done, closeAll, nil
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
