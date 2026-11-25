package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

// A first boot requests every browser name. Later boots reuse the retained leaf,
// and a DNS failure during renewal preserves it and records a durable outcome.
func TestManagedBrowserCertificateReusesLineageAfterDNSFailure(t *testing.T) {
	now := time.Now()
	roots := x509.NewCertPool()
	fixture := browserCertificateFixture(t, roots, now, time.Hour)
	hosts, err := config.NewServiceHosts("https://management.test", "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	installation := t.TempDir()
	manager := newBrowserACME(installation, "admin@example.com", hosts, roots)
	calls := 0
	manager.run = func(_ context.Context, args []string) error {
		calls++
		if args[0] == "renew" {
			return errors.New("access denied by Route 53")
		}
		for _, name := range []string{"management.test", "*.ide.test", "*.preview.test", "*.repair.test"} {
			if !slices.Contains(args, name) {
				t.Fatalf("issuance omitted %s", name)
			}
		}
		if err := os.MkdirAll(manager.lineage(), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(manager.lineage(), "fullchain.pem"), fixture.ChainPEM, 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(manager.lineage(), "privkey.pem"), fixture.KeyPEM, 0600)
	}
	for range 2 {
		if _, _, err := manager.ensure(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("Certbot runs = %d, want one initial order", calls)
	}
	controller := &ControllerState{browserACME: manager}
	if err := controller.renewBrowserCertificate(t.Context(), (&backup.WriteFence{}).Enter); err == nil {
		t.Fatal("renewal should return the DNS error")
	}
	reopened := newBrowserACME(installation, "admin@example.com", hosts, roots)
	if result := reopened.result(); result == nil || result.Result != "failed" || result.At.IsZero() {
		t.Fatalf("renewal result after restart = %+v", result)
	}
	if _, _, err := reopened.load(); err != nil {
		t.Fatalf("renewal failure damaged the retained certificate: %v", err)
	}
}

// A successful renewal must update new TLS handshakes without restarting the
// listener and leave the same certificate in the durable active configuration.
func TestManagedBrowserRenewalReplacesCertificateOnLiveListener(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	settings := config.InstallationSettings{OrganizationName: "ShiftBox", DataProfile: "sqlite",
		ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test",
		PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400}
	dir := t.TempDir()
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(dir, "installation.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: settings.OrganizationName, Origin: settings.ManagementOrigin,
		IDEPattern: settings.IDEHostPattern, PreviewPattern: settings.PreviewHostPattern, Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	old := browserCertificateFixture(t, roots, now, time.Hour)
	renewed := browserCertificateFixture(t, roots, now, 90*24*time.Hour)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := config.ActivateBrowserCertificate(ctx, tx, config.Activation{InstallationID: id, ActorID: "controller", ActivatedAt: now}, settings, old, cipher, roots); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	oldPair, err := config.LoadBrowserCertificate(ctx, db, id, old.ID, cipher, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := config.NewServiceHosts(settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern)
	if err != nil {
		t.Fatal(err)
	}
	manager := newBrowserACME(dir, "admin@example.com", hosts, roots)
	manager.run = func(_ context.Context, args []string) error {
		if args[0] != "renew" {
			t.Fatalf("Certbot command = %q, want renew", args[0])
		}
		if err := os.MkdirAll(manager.lineage(), 0700); err != nil {
			return err
		}
		return errors.Join(os.WriteFile(filepath.Join(manager.lineage(), "fullchain.pem"), renewed.ChainPEM, 0600),
			os.WriteFile(filepath.Join(manager.lineage(), "privkey.pem"), renewed.KeyPEM, 0600))
	}
	state := &ControllerState{DB: db, Installation: ActiveInstallation{InstallationID: id, Settings: settings},
		BrowserTLS: oldPair, browserACME: manager, cipher: cipher, browserRoots: roots}
	server, err := ListenBrowser("127.0.0.1:0", state, http.NotFoundHandler(), &proxy.Tunnels{Registry: &proxy.Registry{}}, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveCtx) }()
	t.Cleanup(func() { stop(); <-done })
	dial := func() []byte {
		t.Helper()
		dialer := tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: "management.test", MinVersion: tls.VersionTLS13}}
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		conn, err := dialer.DialContext(dialCtx, "tcp", server.Address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.(*tls.Conn).ConnectionState().PeerCertificates[0].Raw
	}
	if !bytes.Equal(dial(), oldPair.Leaf.Raw) {
		t.Fatal("first handshake did not use the original certificate")
	}
	if err := state.renewBrowserCertificate(ctx, (&backup.WriteFence{}).Enter); err != nil {
		t.Fatal(err)
	}
	var activeID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM browser_certificates WHERE state='active'`).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	active, err := config.LoadBrowserCertificate(ctx, db, id, activeID, cipher, roots, now)
	if err != nil {
		t.Fatal(err)
	}
	if activeID == old.ID || !bytes.Equal(dial(), active.Leaf.Raw) || manager.result().Result != "succeeded" {
		t.Fatal("renewal did not update both the active record and the live listener")
	}
}
