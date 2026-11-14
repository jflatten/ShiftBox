package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSetupRejectsUnknownSecretFlag keeps unrecognized arguments out of diagnostics.
func TestSetupRejectsUnknownSecretFlag(t *testing.T) {
	var out, status bytes.Buffer
	err := run(context.Background(), []string{"setup", "--password=sentinel-secret"}, &out, &status)
	if err == nil || strings.Contains(err.Error()+out.String()+status.String(), "sentinel-secret") {
		t.Fatal("setup accepted or echoed an unsupported credential argument")
	}
}

// TestSetupCommandResumes starts native Dex and verifies that a second setup run
// serves the same installation without revealing the initial password again.
func TestSetupCommandResumes(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	dir := filepath.Join(t.TempDir(), "installation")
	args := []string{"setup", "--installation-dir", dir, "--administrator-email", "owner@example.test", "--dex-binary", binary}
	var firstPassword string
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		var out bytes.Buffer
		status := &stopOnSetupURL{cancel: cancel, dir: dir}
		err := run(ctx, args, &out, status)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if status.probeErr != nil {
			t.Fatal(status.probeErr)
		}
		if !status.reached {
			t.Fatal("setup did not serve its advertised loopback URL")
		}
		if attempt == 0 {
			firstPassword = strings.TrimSpace(out.String())
			if len(firstPassword) < 24 || strings.Contains(status.String(), firstPassword) {
				t.Fatal("setup failed to deliver the initial password on the data stream only")
			}
		} else if out.Len() != 0 || strings.Contains(status.String(), firstPassword) {
			t.Fatal("setup restart redisplayed the initial password")
		}
	}
}

type stopOnSetupURL struct {
	bytes.Buffer
	cancel   context.CancelFunc
	dir      string
	reached  bool
	probeErr error
}

func (w *stopOnSetupURL) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	for _, line := range strings.Split(string(p), "\n") {
		if origin, ok := strings.CutPrefix(line, "Local setup: "); ok {
			w.probeErr = w.probe(origin)
			w.reached = w.probeErr == nil
			w.cancel()
		}
	}
	return n, err
}

func (w *stopOnSetupURL) probe(origin string) error {
	material, err := os.ReadFile(filepath.Join(w.dir, "bootstrap-tls.pem"))
	if err != nil {
		return err
	}
	public, _ := pem.Decode(material)
	if public == nil || public.Type != "CERTIFICATE" {
		return fmt.Errorf("missing public setup certificate")
	}
	cert, err := x509.ParseCertificate(public.Bytes)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(origin + "/api/v1/setup/status")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("setup status = %d, want 200", response.StatusCode)
	}
	return nil
}
