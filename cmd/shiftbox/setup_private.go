package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
)

func runOnePass(ctx context.Context, args []string, out, status io.Writer) error {
	if len(args) != 4 || !filepath.IsAbs(args[3]) {
		return errors.New("usage: shiftbox one-pass <installation-directory> <config> <postgres-identity-config> <claim-file>")
	}
	err := runSetup(ctx, []string{
		"--installation-dir", args[0], "--config", args[1], "--postgres-identity-config", args[2], "--private-claim-file", args[3],
	}, out, status)
	if err != nil || ctx.Err() != nil {
		return err
	}
	return runServe(ctx, []string{"--config", args[1]})
}

func readPrivateSetupClaim(path string) (*app.PrivateSetupClaim, error) {
	invalid := errors.New("cannot consume the private setup claim file")
	info, err := os.Lstat(path)
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
		return nil, invalid
	}
	code, readErr := os.ReadFile(path)
	defer clear(code)
	if removeErr := os.Remove(path); readErr != nil || removeErr != nil {
		return nil, invalid
	}
	return app.NewPrivateSetupClaim(string(code))
}

func loadPrivateSetup(cloud *app.CloudSetup, roots *x509.CertPool, claim *app.PrivateSetupClaim) (tls.Certificate, error) {
	chain, chainErr := os.ReadFile("/run/shiftbox/browser-tls/server.crt")
	key, keyErr := os.ReadFile("/run/shiftbox/browser-tls/server.key")
	if chainErr != nil || keyErr != nil {
		return tls.Certificate{}, errors.New("cannot load the deployed browser TLS identity")
	}
	hosts, err := config.NewServiceHosts(cloud.ManagementOrigin, cloud.IDEHostPattern, cloud.PreviewHostPattern)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := hosts.ValidateCertificate(chain, key, roots, time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	cloud.PrivateClaim = claim
	cloud.BrowserCertificate = &config.BrowserCertificateInput{ChainPEM: chain, KeyPEM: key}
	return cert, nil
}
