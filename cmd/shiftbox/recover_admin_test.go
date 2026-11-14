package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"golang.org/x/crypto/bcrypt"
)

// TestRecoverAdminCommand uses retained Dex and verifies the local lock,
// recovery without browser TLS, and rejection of the old credential.
func TestRecoverAdminCommand(t *testing.T) {
	binary := os.Getenv("SHIFTBOX_TEST_DEX_BINARY")
	if binary == "" {
		t.Skip("set SHIFTBOX_TEST_DEX_BINARY to the pinned Dex binary")
	}
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "installation")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := setupCipher(dir); err != nil {
		t.Fatal(err)
	}
	cert, err := app.EnsureBootstrapCertificate(filepath.Join(dir, "bootstrap-tls.pem"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{InstallationDir: dir, DataProfile: "sqlite", SQLitePath: filepath.Join(dir, "bootstrap.db"),
		ManagementOrigin: "https://management.test", ExternalAuthIntervalSeconds: 14400}
	configPath := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	db, id, err := database.OpenBootstrap(ctx, cfg.SQLitePath, database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: cfg.ManagementOrigin,
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "test", MinGuestRelease: "test", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := uuid.NewString()
	const email = "owner@example.test"
	const oldPassword = "fixture-old-password"
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,$2,'administrator','active','local','setup','now','now',1)`, user, email); err != nil {
		t.Fatal(err)
	}
	const settings = `{"browserCertificateId":"unavailable","organizationName":"ShiftBox","dataProfile":"sqlite","managementOrigin":"https://management.test","ideHostPattern":"{workspace}.ide.test","previewHostPattern":"{service}--{workspace}.preview.test","externalAuthIntervalSeconds":14400}`
	if _, err := db.ExecContext(ctx, "INSERT INTO activated_configurations(installation_id,generation,settings_json,activated_by,activated_at) VALUES($1,1,$2,$3,'now')", id, settings, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active',config_generation=1 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	seedDex, _, _, closeSeed, err := startSetupDex(ctx, dir, binary, "https://127.0.0.1:9999", cert, nil)
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
	admin, _, _, _, closeDex, err := startControllerDex(ctx, dir, binary, cfg.ManagementOrigin, nil)
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
}
