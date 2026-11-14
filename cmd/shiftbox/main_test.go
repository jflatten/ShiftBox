package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
		status := &stopOnSetupURL{cancel: cancel, dir: dir, password: &out}
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
	password *bytes.Buffer
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
	if w.password.Len() == 0 {
		return nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client.Jar = jar
	unbound, err := client.Get(origin + "/auth/callback?code=unexpected&state=unbound")
	if err != nil {
		return err
	}
	unbound.Body.Close()
	if unbound.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unbound callback status = %d, want 401", unbound.StatusCode)
	}
	browser, err := client.Get(origin + "/auth/login")
	if err != nil {
		return err
	}
	browser, err = submitDexForm(client, browser, url.Values{
		"login": {"owner@example.test"}, "password": {strings.TrimSpace(w.password.String())},
	})
	if err != nil {
		return err
	}
	if strings.HasSuffix(browser.Request.URL.Path, "/approval") {
		browser, err = submitDexForm(client, browser, url.Values{"approval": {"approve"}})
		if err != nil {
			return err
		}
	}
	defer browser.Body.Close()
	body, err := io.ReadAll(browser.Body)
	if err != nil {
		return err
	}
	if browser.StatusCode != 200 || browser.Request.URL.Path != "/api/v1/session" ||
		!bytes.Contains(body, []byte(`"mode":"bootstrap_restricted"`)) ||
		!bytes.Contains(body, []byte(`"mustChangePassword":true`)) ||
		bytes.Contains(body, []byte("setup.mutate")) {
		return fmt.Errorf("Dex login did not produce a password-gated bootstrap session (status %d)", browser.StatusCode)
	}
	return nil
}

// submitDexForm drives the pinned Dex HTML login and approval forms.
func submitDexForm(client *http.Client, response *http.Response, fields url.Values) (*http.Response, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	action := response.Request.URL
	form := regexp.MustCompile(`(?i)<form\b[^>]*action="([^"]+)"`).FindSubmatch(body)
	if len(form) == 2 {
		action, err = action.Parse(html.UnescapeString(string(form[1])))
		if err != nil {
			return nil, err
		}
	} else if fields.Get("approval") == "" {
		return nil, fmt.Errorf("Dex form was unavailable at %s (status %d)", action.Path, response.StatusCode)
	}
	req := regexp.MustCompile(`name="req" value="([^"]+)"`).FindSubmatch(body)
	if len(req) == 2 {
		fields.Set("req", html.UnescapeString(string(req[1])))
	}
	return client.PostForm(action.String(), fields)
}
