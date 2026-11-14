package config_test

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/migrations"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCertificateActivationAtomic protects the last usable certificate and configuration when replacement fails, and persists only encrypted keys.
func TestCertificateActivationAtomic(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "cert.db"), Schema: "shiftbox_test"}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL fixture not configured")
				}
			}
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			ddl, err := migrations.Files.ReadFile(profile + "/00001_foundation.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, strings.ReplaceAll(string(ddl), "CREATE TABLE ", "CREATE TEMPORARY TABLE ")); err != nil {
				t.Fatal(err)
			}
			const installation = "00000000-0000-4000-8000-000000000001"
			if _, err := db.Exec(`INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES($1,'configuring','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`, installation); err != nil {
				t.Fatal(err)
			}
			cipher, err := cryptostore.New(make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			chain, key, roots, now := browserCertificateFixture(t, x509.ExtKeyUsageServerAuth)
			input := config.BrowserCertificateInput{ID: "00000000-0000-4000-8000-000000000002", ChainPEM: chain, KeyPEM: key}
			activation := config.Activation{InstallationID: installation, ActorID: "setup", ActivatedAt: now}
			settings := config.InstallationSettings{OrganizationName: "ShiftBox", DataProfile: "sqlite", ManagementOrigin: "https://management.test", IDEHostPattern: "{workspace}.ide.test", PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400}
			apply := func(tx *sql.Tx) error {
				_, err := config.ActivateBrowserCertificate(ctx, tx, activation, settings, input, cipher, roots)
				return err
			}
			if err := database.Transaction(ctx, db, apply); err != nil {
				t.Fatal(err)
			}
			var encrypted string
			if err := db.QueryRow(`SELECT value_json FROM protected_values`).Scan(&encrypted); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encrypted, "PRIVATE KEY") || strings.Contains(encrypted, string(key)) {
				t.Fatal("protected storage contains plaintext key")
			}
			loaded, err := config.LoadBrowserCertificate(ctx, db, installation, input.ID, cipher, roots, now)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.PrivateKey == nil {
				t.Fatal("persisted certificate cannot be loaded")
			}
			oldID := input.ID
			activation.ExpectedGeneration = 1
			input.ID = "00000000-0000-4000-8000-000000000003"
			input.ChainPEM, input.KeyPEM, roots, _ = browserCertificateFixture(t, x509.ExtKeyUsageServerAuth)
			failure := errors.New("later activation failed")
			if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
				if err := apply(tx); err != nil {
					return err
				}
				return failure
			}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			var activeID string
			var generation, keys int
			if err := db.QueryRow(`SELECT id FROM browser_certificates WHERE state='active'`).Scan(&activeID); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT config_generation FROM installations`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM protected_values`).Scan(&keys); err != nil {
				t.Fatal(err)
			}
			if activeID != oldID || generation != 1 || keys != 1 {
				t.Fatalf("failed replacement active/generation/keys=%s/%d/%d, want %s/1/1", activeID, generation, keys, oldID)
			}
			if err := database.Transaction(ctx, db, apply); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT id FROM browser_certificates WHERE state='active'`).Scan(&activeID); err != nil {
				t.Fatal(err)
			}
			if activeID != input.ID {
				t.Errorf("replacement active = %s, want %s", activeID, input.ID)
			}
			if _, err := config.LoadBrowserCertificate(ctx, db, installation, oldID, cipher, roots, now); err == nil {
				t.Error("retired certificate was loaded")
			}
			if _, err := config.LoadBrowserCertificate(ctx, db, installation, input.ID, cipher, roots, now.Add(2*time.Hour)); err == nil {
				t.Error("expired active certificate was loaded")
			}
			activation.ExpectedGeneration = 2
			input.ID = "00000000-0000-4000-8000-000000000004"
			input.KeyPEM = []byte("invalid private key")
			if err := database.Transaction(ctx, db, apply); !errors.Is(err, config.ErrInvalidBrowserCertificate) {
				t.Fatalf("invalid certificate activation = %v, want rejection", err)
			}
			if err := db.QueryRow(`SELECT config_generation FROM installations`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if generation != 2 {
				t.Errorf("invalid upload advanced generation to %d, want 2", generation)
			}
			input.ID = activeID
			wrongCipher, _ := cryptostore.New([]byte(strings.Repeat("x", 32)))
			if _, err := config.LoadBrowserCertificate(ctx, db, installation, input.ID, wrongCipher, roots, now); err == nil {
				t.Error("wrong installation key loaded certificate")
			}
			if _, err := config.LoadBrowserCertificate(ctx, db, "other-installation", input.ID, cipher, roots, now); err == nil {
				t.Error("different installation loaded certificate")
			}
		})
	}
}
