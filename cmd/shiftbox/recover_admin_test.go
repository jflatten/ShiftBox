package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"golang.org/x/crypto/bcrypt"
)

// TestRecoverAdminCommand uses retained Dex and verifies the local lock,
// recovery without browser TLS, and rejection of the old credential.
func TestRecoverAdminCommand(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { testRecoverAdminCommand(t, "", "") })
	t.Run("postgres", func(t *testing.T) {
		appURI, dexURI := os.Getenv("SHIFTBOX_TEST_RECOVERY_APP_URI"), os.Getenv("SHIFTBOX_TEST_RECOVERY_DEX_URI")
		if appURI == "" || dexURI == "" {
			t.Skip("set the scoped disposable PostgreSQL recovery fixture URIs")
		}
		testRecoverAdminCommand(t, appURI, dexURI)
	})
}

func testRecoverAdminCommand(t *testing.T, appURI, dexURI string) {
	t.Helper()
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "installation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cipher, err := setupCipher(dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{InstallationDir: dir, DataProfile: "sqlite", SQLitePath: filepath.Join(dir, "bootstrap.db"),
		ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	var postgres *identity.DexPostgres
	if appURI != "" {
		cfg.DataProfile, cfg.SQLitePath, cfg.PostgresURI = "postgres", "", appURI
		postgres = recoveryDexFixture(t, dexURI)
	}
	configPath := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, database.Config{Profile: cfg.DataProfile, SQLitePath: cfg.SQLitePath,
		PostgresURI: cfg.PostgresURI, Schema: "shiftbox"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, cfg.DataProfile); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,
		preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'configuring','ShiftBox',$2,$3,'{workspace}.ide.test','{service}--{workspace}.preview.test','test','test','now','now',1)`,
		id, cfg.DataProfile, cfg.ManagementOrigin); err != nil {
		t.Fatal(err)
	}
	if postgres != nil {
		recordID, encoded := encryptRecoveryDexFixture(t, cipher, id, *postgres)
		if _, err := db.ExecContext(ctx, "INSERT INTO protected_values(id,value_json) VALUES($1,$2)", recordID, encoded); err != nil {
			t.Fatal(err)
		}
	}
	user := uuid.NewString()
	const email = "owner@example.test"
	const oldPassword = "fixture-old-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,$2,'administrator','active','local','setup','now','now',1)`, user, email); err != nil {
		t.Fatal(err)
	}
	settings, err := json.Marshal(config.InstallationSettings{BrowserCertificateID: "unavailable", OrganizationName: "ShiftBox",
		DataProfile: cfg.DataProfile, ManagementOrigin: cfg.ManagementOrigin, IDEHostPattern: "{workspace}.ide.test",
		PreviewHostPattern: "{service}--{workspace}.preview.test", ExternalAuthIntervalSeconds: 14400})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at) VALUES($1,1,$2,$3,'now')", id, string(settings), user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active',config_generation=1 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	seedDex, _, _, closeSeed, err := startSetupDex(ctx, dir, binary, "https://127.0.0.1:9999", cert, postgres)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)
	if err == nil {
		err = seedDex.Ensure(ctx, email, user, hash)
	}
	closeSeed()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"recover-admin", "--config", configPath, "--email", email, "--dex-binary", binary}
	lock, err := lockSetup(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = run(ctx, args, &out, io.Discard)
	lock.Close()
	if err == nil || out.Len() != 0 {
		t.Fatal("recovery succeeded while installation lock was held")
	}
	if err := run(ctx, args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	admin, _, _, _, closeDex, err := startControllerDex(ctx, dir, binary, cfg.ManagementOrigin, postgres)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDex()
	newPassword := strings.TrimSpace(out.String())
	oldValid, oldErr := admin.PasswordVerified(ctx, email, oldPassword)
	newValid, newErr := admin.PasswordVerified(ctx, email, newPassword)
	if oldErr != nil || oldValid || newErr != nil || !newValid || len(newPassword) < 24 {
		t.Fatal("Dex did not replace the old administrator password")
	}
	if postgres != nil {
		if _, err := os.Lstat(filepath.Join(dir, "dex.db")); !os.IsNotExist(err) {
			t.Fatal("PostgreSQL recovery did not leave local Dex fallback absent")
		}
	}
}

func recoveryDexFixture(t *testing.T, raw string) *identity.DexPostgres {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		t.Fatal("invalid PostgreSQL recovery identity fixture")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil {
		t.Fatal("invalid PostgreSQL recovery fixture port")
	}
	password, _ := u.User.Password()
	return &identity.DexPostgres{Host: u.Hostname(), Port: uint16(port), Database: strings.TrimPrefix(u.Path, "/"),
		User: u.User.Username(), Password: password, CAFile: os.Getenv("SHIFTBOX_TEST_SETUP_CA")}
}

func encryptRecoveryDexFixture(t *testing.T, cipher *cryptostore.Store, installation string, postgres identity.DexPostgres) (string, string) {
	t.Helper()
	// Seed only the retained scoped Dex record the production opener consumes.
	recordID := installation + ":dex-postgres"
	plain, err := json.Marshal(postgres)
	if err != nil {
		t.Fatal("cannot encode PostgreSQL recovery fixture")
	}
	defer clear(plain)
	sealed, err := cipher.Seal(cryptostore.Context{KeyVersion: 1, RecordType: "dex_postgres", Scope: "installation",
		OwnerID: installation, RecordID: recordID, Name: "scoped_credentials"}, plain)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return recordID, string(encoded)
}
