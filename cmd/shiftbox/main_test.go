package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
)

// TestSetupRejectsUnknownSecretFlag keeps unrecognized arguments out of diagnostics.
func TestSetupRejectsUnknownSecretFlag(t *testing.T) {
	var out, status bytes.Buffer
	err := run(context.Background(), []string{"setup", "--password=sentinel-secret"}, &out, &status)
	if err == nil || strings.Contains(err.Error()+out.String()+status.String(), "sentinel-secret") {
		t.Fatal("setup accepted or echoed an unsupported credential argument")
	}
}

// TestServeRejectsBootstrapInstallation verifies that the real command cannot
// treat a resumable local setup as an activated installation or announce readiness.
func TestServeRejectsBootstrapInstallation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "installation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "controller.json")
	settings := config.Config{InstallationDir: dir, DataProfile: "sqlite", SQLitePath: filepath.Join(dir, "bootstrap.db"),
		ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	const installationID = "00000000-0000-4000-8000-000000000001"
	db, _, err := database.OpenBootstrap(t.Context(), settings.SQLitePath, database.BootstrapSeed{
		ID: installationID, Organization: "Fixture", Origin: "https://127.0.0.1:8443", IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: controllerRelease, MinGuestRelease: controllerRelease, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = run(t.Context(), []string{"serve", "--config", configPath}, &stdout, &stderr)
	if !errors.Is(err, app.ErrActiveDatabase) || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("serve against bootstrapping state = %v and output lengths %d/%d, want inactive installation and no output", err, stdout.Len(), stderr.Len())
	}
	var state string
	if err := db.QueryRowContext(t.Context(), "SELECT state FROM installations WHERE id=$1", installationID).Scan(&state); err != nil || state != "bootstrap" {
		t.Fatalf("serve left installation in %q (%v), want setup still resumable", state, err)
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
	if dexURI := os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI"); dexURI != "" && os.Getenv("SHIFTBOX_TEST_SETUP_APP_URI") != "" {
		configPath := filepath.Join(t.TempDir(), "identity.json")
		raw, err := json.Marshal(map[string]string{"dexUri": dexURI, "caFile": os.Getenv("SHIFTBOX_TEST_SETUP_CA")})
		if err != nil {
			t.Fatal("cannot encode identity fixture")
		}
		if err := os.WriteFile(configPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--postgres-identity-config", configPath)
	}
	var firstPassword string
	var firstCA []byte
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
		db, err := sql.Open("sqlite", filepath.Join(dir, "bootstrap.db"))
		if err != nil {
			t.Fatal(err)
		}
		var active, minimum string
		err = db.QueryRow("SELECT active_release,min_guest_release FROM installations").Scan(&active, &minimum)
		db.Close()
		if err != nil || active != controllerRelease || minimum != controllerRelease {
			t.Fatalf("setup release tuple = %q/%q, %v; want the built controller release", active, minimum, err)
		}
		caRecord, err := os.ReadFile(filepath.Join(dir, "guest-ca.json"))
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			firstCA = caRecord
		} else if !bytes.Equal(firstCA, caRecord) {
			t.Fatal("setup retry changed the installation guest CA")
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

// TestSetupResetsLostTemporaryPassword recovers an unconsumed credential
// through explicit local invocation and signs in using its replacement.
func TestSetupResetsLostTemporaryPassword(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	dir := filepath.Join(t.TempDir(), "installation")
	args := []string{"setup", "--installation-dir", dir, "--administrator-email", "owner@example.test", "--dex-binary", binary}
	initialCtx, stopInitial := context.WithTimeout(t.Context(), 20*time.Second)
	defer stopInitial()
	var initial bytes.Buffer
	initialStatus := &stopOnSetupURL{cancel: stopInitial, dir: dir, password: &bytes.Buffer{}}
	if err := run(initialCtx, args, &initial, initialStatus); err != nil || initialStatus.probeErr != nil || !initialStatus.reached {
		t.Fatalf("initial setup = %v, probe %v, reached %t", err, initialStatus.probeErr, initialStatus.reached)
	}
	firstPassword := strings.TrimSpace(initial.String())
	if len(firstPassword) < 24 {
		t.Fatal("initial setup did not issue a password")
	}
	var reset bytes.Buffer
	if err := run(t.Context(), append(append([]string(nil), args...), "--reset-temporary-password"), &reset, io.Discard); err != nil {
		t.Fatal(err)
	}
	newPassword := strings.TrimSpace(reset.String())
	if len(newPassword) < 24 || newPassword == firstPassword {
		t.Fatal("protected reset did not issue a replacement")
	}
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dexCtx, stopDex := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopDex()
	admin, _, _, closeDex, err := startSetupDex(dexCtx, dir, binary, "https://127.0.0.1:9999", cert, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldAccepted, err := admin.PasswordVerified(dexCtx, "owner@example.test", firstPassword)
	closeDex()
	if err != nil || oldAccepted {
		t.Fatalf("old temporary password accepted = %t (%v), want false", oldAccepted, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	status := &stopOnSetupURL{cancel: cancel, dir: dir, password: &reset}
	if err := run(ctx, args, &out, status); err != nil || status.probeErr != nil || !status.reached || out.Len() != 0 {
		t.Fatalf("setup after reset = %v, probe %v, reached %t, output length %d", err, status.probeErr, status.reached, out.Len())
	}
	reset.Reset()
	if err := run(t.Context(), append(append([]string(nil), args...), "--reset-temporary-password"), &reset, io.Discard); err == nil || reset.Len() != 0 {
		t.Fatal("temporary password reset must refuse an administrator who has already changed the initial password")
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
		browser.Body.Close()
		return errors.New("local setup login displayed a separate consent screen")
	}
	browser, err = readSetupSession(client, origin, browser)
	if err != nil {
		return err
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
	var session struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return err
	}
	selectProfile := func(originHeader string, want int) ([]byte, error) {
		req, err := http.NewRequest(http.MethodPut, origin+"/api/v1/setup/data-profile", strings.NewReader(`{"profile":"sqlite"}`))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", originHeader)
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		req.Header.Set("Idempotency-Key", "choose-local-sqlite-001")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			return nil, fmt.Errorf("setup profile status = %d, want %d", res.StatusCode, want)
		}
		return io.ReadAll(res.Body)
	}
	if _, err := selectProfile(origin, http.StatusForbidden); err != nil {
		return err
	}
	current := strings.TrimSpace(w.password.String())
	replacement := "integration-new-password-2026"
	changePassword := func(current, requestOrigin string, want int) error {
		payload, err := json.Marshal(map[string]string{"currentPassword": current, "newPassword": replacement})
		if err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodPut, origin+"/api/v1/session/password", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", requestOrigin)
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		result, err := client.Do(req)
		if err != nil {
			return err
		}
		result.Body.Close()
		if result.StatusCode != want {
			return fmt.Errorf("password change status = %d, want %d", result.StatusCode, want)
		}
		return nil
	}
	if err := changePassword(current, "https://other.example", http.StatusForbidden); err != nil {
		return err
	}
	if err := changePassword("wrong-current-password", origin, http.StatusForbidden); err != nil {
		return err
	}

	// Inject a controller write failure after Dex accepts the replacement.
	db, err := sql.Open("sqlite", filepath.Join(w.dir, "bootstrap.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER interrupt_password_change BEFORE UPDATE OF must_change_password ON users
		BEGIN SELECT RAISE(ABORT, 'test interruption'); END`); err != nil {
		return err
	}
	if err := changePassword(current, origin, http.StatusInternalServerError); err != nil {
		return err
	}
	if _, err := db.Exec("DROP TRIGGER interrupt_password_change"); err != nil {
		return err
	}
	if err := changePassword(current, origin, http.StatusNoContent); err != nil {
		return err
	}
	var ownerID string
	if err := db.QueryRow("SELECT user_id FROM bootstrap_administrator").Scan(&ownerID); err != nil {
		return err
	}
	result, err := client.Get(origin + "/api/v1/session")
	if err != nil {
		return err
	}
	result.Body.Close()
	if result.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("old setup session status = %d, want 401", result.StatusCode)
	}
	browser, err = client.Get(origin + "/auth/login")
	if err != nil {
		return err
	}
	browser, err = submitDexForm(client, browser, url.Values{"login": {"owner@example.test"}, "password": {replacement}})
	if err != nil {
		return err
	}
	if strings.HasSuffix(browser.Request.URL.Path, "/approval") {
		browser.Body.Close()
		return errors.New("local setup login displayed a separate consent screen")
	}
	browser, err = readSetupSession(client, origin, browser)
	if err != nil {
		return err
	}
	defer browser.Body.Close()
	body, err = io.ReadAll(browser.Body)
	if err != nil {
		return err
	}
	if browser.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"mustChangePassword":false`)) ||
		!bytes.Contains(body, []byte(`"setup.mutate"`)) {
		return fmt.Errorf("fresh sign-in did not unlock setup")
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return err
	}
	if appURI := os.Getenv("SHIFTBOX_TEST_SETUP_APP_URI"); appURI != "" && os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI") != "" {
		raw, err := json.Marshal(map[string]string{"profile": "postgres", "connectionUri": appURI, "tlsMode": "verify-full"})
		if err != nil {
			return fmt.Errorf("cannot encode profile fixture")
		}
		req, err := http.NewRequest(http.MethodPut, origin+"/api/v1/setup/data-profile", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		req.Header.Set("Idempotency-Key", "choose-postgres-0001")
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			return fmt.Errorf("PostgreSQL profile status = %d, want 202", res.StatusCode)
		}
	}
	if err := verifyCredentialTransfer(w.dir, origin, filepath.Join(w.dir, "dex.db"), ownerID, current, replacement); err != nil {
		return err
	}
	if _, err := selectProfile("https://other.example", http.StatusForbidden); err != nil {
		return err
	}
	first, err := selectProfile(origin, http.StatusAccepted)
	if err != nil {
		return err
	}
	second, err := selectProfile(origin, http.StatusAccepted)
	if err != nil {
		return err
	}
	if !bytes.Equal(first, second) || !bytes.Contains(first, []byte(`"status":"succeeded"`)) {
		return fmt.Errorf("SQLite profile did not return the same completed operation on retry")
	}
	result, err = client.Get(origin + "/api/v1/setup/status")
	if err != nil {
		return err
	}
	defer result.Body.Close()
	body, err = io.ReadAll(result.Body)
	if err != nil {
		return err
	}
	if !bytes.Contains(body, []byte(`"availableSteps":["certificate"]`)) ||
		!bytes.Contains(body, []byte(`"state":"configuring"`)) {
		return fmt.Errorf("setup status did not advance to certificate configuration")
	}

	return nil
}

// readSetupSession verifies that local login returned to the setup page.
func readSetupSession(client *http.Client, origin string, response *http.Response) (*http.Response, error) {
	if response.Request.URL.Path == "/api/v1/session" {
		return response, nil
	}
	defer response.Body.Close()
	if response.Request.URL.Path != "/setup/" || response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("setup callback returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Contains(body, []byte("Local setup")) {
		return nil, fmt.Errorf("setup callback did not serve the embedded setup page")
	}
	return client.Get(origin + "/api/v1/session")
}

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
	} else {
		return nil, fmt.Errorf("Dex form was unavailable at %s (status %d)", action.Path, response.StatusCode)
	}
	return client.PostForm(action.String(), fields)
}

// verifyCredentialTransfer imports into an independent native Dex and verifies
// the replacement there. The source continues serving the bootstrap session.
func verifyCredentialTransfer(dir, origin, source, ownerID, temporary, replacement string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target, err := os.MkdirTemp(dir, "transfer-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(target)
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		return err
	}
	var postgres *identity.DexPostgres
	if fixture := os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI"); fixture != "" {
		u, err := url.Parse(fixture)
		if err != nil || u.User == nil {
			return fmt.Errorf("invalid PostgreSQL Dex fixture")
		}
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil {
			return fmt.Errorf("invalid PostgreSQL Dex fixture port")
		}
		password, _ := u.User.Password()
		postgres = &identity.DexPostgres{Host: u.Hostname(), Port: uint16(port),
			Database: strings.TrimPrefix(u.Path, "/"), User: u.User.Username(), Password: password,
			CAFile: os.Getenv("SHIFTBOX_TEST_SETUP_CA")}
	}
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if postgres != nil {
		untrusted := *postgres
		untrusted.CAFile = filepath.Join(target, "missing-ca.pem")
		if _, _, _, cleanup, err := startSetupDex(ctx, target, binary, origin, cert, &untrusted); err == nil {
			cleanup()
			return fmt.Errorf("Dex startup accepted missing PostgreSQL trust")
		}
	}
	admin, _, _, closeDex, err := startSetupDex(ctx, target, binary, origin, cert, postgres)
	if err != nil {
		return err
	}
	defer closeDex()
	const email = "owner@example.test"
	if err := identity.TransferBootstrapCredential(ctx, source, email, "wrong-owner", admin); err == nil {
		return fmt.Errorf("Dex transfer accepted a different owner ID")
	}
	for range 2 {
		if err := identity.TransferBootstrapCredential(ctx, source, email, ownerID, admin); err != nil {
			return err
		}
	}
	for password, want := range map[string]bool{temporary: false, replacement: true} {
		ok, err := admin.PasswordVerified(ctx, email, password)
		if err != nil || ok != want {
			return fmt.Errorf("Dex transfer did not preserve the changed administrator credential")
		}
	}
	if postgres != nil {
		if _, err := os.Lstat(filepath.Join(target, "dex.db")); !os.IsNotExist(err) {
			return fmt.Errorf("PostgreSQL Dex created a SQLite fallback")
		}
		uri, err := url.Parse(os.Getenv("SHIFTBOX_TEST_SETUP_DEX_URI"))
		if err != nil {
			return fmt.Errorf("invalid PostgreSQL Dex fixture")
		}
		query := uri.Query()
		query.Set("sslrootcert", postgres.CAFile)
		uri.RawQuery = query.Encode()
		db, err := sql.Open("pgx", uri.String())
		if err != nil {
			return fmt.Errorf("cannot open PostgreSQL verification connection")
		}
		defer db.Close()
		var inDex, inPublic bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass('dex.password') IS NOT NULL,
			to_regclass('public.password') IS NOT NULL`).Scan(&inDex, &inPublic); err != nil || !inDex || inPublic {
			return fmt.Errorf("Dex migrations did not land exclusively in the Dex schema")
		}
	}

	return nil
}

// TestCloudBootstrapDeliversPasswordOnlyOverTLS verifies the one-off task can
// create its administrator and sign in entirely through the forwarded wizard.
func TestCloudBootstrapDeliversPasswordOnlyOverTLS(t *testing.T) {
	dex := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if dex == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	dir := filepath.Join(t.TempDir(), "installation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		InstallationDir: dir, DataProfile: "postgres", ManagementOrigin: "https://shiftbox.dev.example.test:8443",
		PostgresURI:                 "postgres://shiftbox_app:fixture@postgres.dev.example.test/shiftbox?sslmode=verify-full",
		ExternalAuthIntervalSeconds: 14400,
		Runtime: &config.ControllerRuntime{Provider: "ec2", BrowserAddress: ":8443", GuestHost: "guest.dev.example.test",
			EnrollmentAddress: ":19443", PollAddress: ":19444", TunnelAddress: ":19445"},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "controller.json")
	dexPath := filepath.Join(dir, "identity.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dexPath, []byte(`{"dexUri":"postgres://shiftbox_dex:fixture@postgres.dev.example.test/shiftbox?sslmode=verify-full"}`), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var out, status bytes.Buffer
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSetup(ctx, []string{"--installation-dir", dir, "--config", path, "--dex-binary", dex,
			"--postgres-identity-config", dexPath}, &out, setupTestWriter(func(p []byte) (int, error) {
			n, err := status.Write(p)
			ready <- struct{}{}
			return n, err
		}))
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("cloud setup exited before serving: %v", err)
	case <-ctx.Done():
		t.Fatal("cloud setup did not announce its loopback listener")
	}
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			<-done
		}
	}()
	certificate, err := os.ReadFile(filepath.Join(dir, "bootstrap-tls.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		t.Fatal("setup certificate cannot be trusted")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	const origin = "https://127.0.0.1:8444"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v1/setup/administrator",
		strings.NewReader(`{"email":"owner@example.test"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "cloud-administrator-fixture")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil || res.StatusCode != http.StatusCreated || len(created.TemporaryPassword) < 24 {
		t.Fatalf("administrator creation returned %d, expected one-time password", res.StatusCode)
	}
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.Host != "127.0.0.1:8444" {
			return errors.New("login redirected outside the forwarded setup origin")
		}
		return nil
	}
	browser, err := client.Get(origin + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	browser, err = submitDexForm(client, browser, url.Values{
		"login": {"owner@example.test"}, "password": {created.TemporaryPassword},
	})
	if err != nil {
		t.Fatal(err)
	}
	browser, err = readSetupSession(client, origin, browser)
	if err != nil {
		t.Fatal(err)
	}
	sessionBody, err := io.ReadAll(browser.Body)
	browser.Body.Close()
	if err != nil || browser.StatusCode != http.StatusOK || !bytes.Contains(sessionBody, []byte(`"mustChangePassword":true`)) {
		t.Fatal("forwarded login did not establish a password-change session")
	}
	cancel()
	err = <-done
	stopped = true
	if err != nil || out.Len() != 0 || strings.Contains(status.String(), created.TemporaryPassword) {
		t.Fatal("cloud setup stopped unexpectedly or logged the temporary password")
	}
}

type setupTestWriter func([]byte) (int, error)

func (write setupTestWriter) Write(p []byte) (int, error) {
	return write(p)
}
