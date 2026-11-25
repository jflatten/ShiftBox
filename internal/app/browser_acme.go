package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

const browserLineage = "shiftbox-browser"

type browserACME struct {
	dir   string
	email string
	hosts *config.ServiceHosts
	roots *x509.CertPool
	run   func(context.Context, []string) error
}

type browserRenewalResult struct {
	Result string    `json:"result"`
	At     time.Time `json:"at"`
}

func newBrowserACME(installation, email string, hosts *config.ServiceHosts, roots *x509.CertPool) *browserACME {
	return &browserACME{dir: filepath.Join(installation, "browser-acme"), email: email, hosts: hosts, roots: roots,
		run: func(ctx context.Context, args []string) error {
			cmd := exec.CommandContext(ctx, "/usr/bin/certbot", args...)
			// Certbot reports actionable ACME and Route 53 errors to the task log.
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			return cmd.Run()
		}}
}

// ObtainManagedBrowserCertificate opens bootstrap HTTPS only after a trusted leaf is available.
func ObtainManagedBrowserCertificate(ctx context.Context, installation, email string, hosts *config.ServiceHosts, roots *x509.CertPool) (config.BrowserCertificateInput, tls.Certificate, error) {
	return newBrowserACME(installation, email, hosts, roots).ensure(ctx)
}

func (a *browserACME) lineage() string {
	return filepath.Join(a.dir, "config", "live", browserLineage)
}

func (a *browserACME) load() (config.BrowserCertificateInput, tls.Certificate, error) {
	chain, chainErr := os.ReadFile(filepath.Join(a.lineage(), "fullchain.pem"))
	key, keyErr := os.ReadFile(filepath.Join(a.lineage(), "privkey.pem"))
	if err := errors.Join(chainErr, keyErr); err != nil {
		clear(key)
		return config.BrowserCertificateInput{}, tls.Certificate{}, err
	}
	pair, err := a.hosts.ValidateCertificate(chain, key, a.roots, time.Now())
	if err != nil {
		clear(key)
		return config.BrowserCertificateInput{}, tls.Certificate{}, err
	}
	return config.BrowserCertificateInput{ChainPEM: chain, KeyPEM: key}, pair, nil
}

func (a *browserACME) ensure(ctx context.Context) (config.BrowserCertificateInput, tls.Certificate, error) {
	if input, pair, err := a.load(); err == nil {
		return input, pair, nil
	}
	subcommand := "certonly"
	if _, err := os.Stat(filepath.Join(a.dir, "config", "renewal", browserLineage+".conf")); err == nil {
		subcommand = "renew"
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.BrowserCertificateInput{}, tls.Certificate{}, err
	}
	if err := a.certbot(ctx, subcommand); err != nil {
		return config.BrowserCertificateInput{}, tls.Certificate{}, err
	}
	return a.load()
}

func (a *browserACME) certbot(ctx context.Context, subcommand string) error {
	if err := os.MkdirAll(a.dir, 0700); err != nil {
		return err
	}
	args := []string{subcommand, "--non-interactive", "--quiet", "--cert-name", browserLineage,
		"--config-dir", filepath.Join(a.dir, "config"), "--work-dir", filepath.Join(a.dir, "work"),
		"--logs-dir", filepath.Join(a.dir, "logs")}
	if subcommand == "certonly" {
		args = append(args, "--dns-route53", "--agree-tos", "--email", a.email)
		for _, name := range a.hosts.CertificateNames() {
			args = append(args, "-d", name)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := a.run(ctx, args); err != nil {
		return fmt.Errorf("Certbot %s failed: %w", subcommand, err)
	}
	return nil
}

func (a *browserACME) result() *browserRenewalResult {
	data, err := os.ReadFile(filepath.Join(a.dir, "renewal-result.json"))
	var result browserRenewalResult
	if err != nil || json.Unmarshal(data, &result) != nil {
		return nil
	}
	return &result
}

func (a *browserACME) record(result string) error {
	file, err := os.CreateTemp(a.dir, ".renewal-result-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(browserRenewalResult{Result: result, At: time.Now().UTC()})
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(a.dir, "renewal-result.json"))
}

// RunBrowserCertificateRenewal lets Certbot decide when the retained lineage is due.
func (s *ControllerState) RunBrowserCertificateRenewal(ctx context.Context, enter func(context.Context) (func(), error)) error {
	if s.browserACME == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	for ctx.Err() == nil {
		err := s.renewBrowserCertificate(ctx, enter)
		wait := 12 * time.Hour
		if err != nil && ctx.Err() == nil {
			slog.Warn("Browser certificate renewal or activation failed", "error", err)
			wait = time.Hour
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (s *ControllerState) renewBrowserCertificate(ctx context.Context, enter func(context.Context) (func(), error)) (err error) {
	a := s.browserACME
	defer func() {
		if err != nil && ctx.Err() == nil {
			err = errors.Join(err, a.record("failed"))
		}
	}()
	if err := a.certbot(ctx, "renew"); err != nil {
		return err
	}
	input, pair, err := a.load()
	if err != nil {
		return err
	}
	defer clear(input.KeyPEM)
	exit, err := enter(ctx)
	if err != nil {
		return err
	}
	defer exit()
	changed, err := s.activateManagedBrowserCertificate(ctx, input, pair)
	if err != nil || !changed {
		return err
	}
	return a.record("succeeded")
}

func (s *ControllerState) activateManagedBrowserCertificate(ctx context.Context, input config.BrowserCertificateInput, pair tls.Certificate) (bool, error) {
	current := &s.BrowserTLS
	if replacement := s.browserReplacement.Load(); replacement != nil {
		current = &replacement.pair
	}
	if current.Leaf != nil && bytes.Equal(current.Leaf.Raw, pair.Leaf.Raw) {
		return false, nil
	}
	var generation int64
	err := database.Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		activation, settings, err := currentBrowserActivation(ctx, tx, "controller", time.Now())
		if err != nil {
			return err
		}
		input.ID = uuid.NewString()
		generation, err = config.ActivateBrowserCertificate(ctx, tx, activation, settings, input, s.cipher, s.browserRoots)
		return err
	})
	if err != nil {
		return false, err
	}
	s.publishBrowserCertificate(generation, pair)
	return true, nil
}
