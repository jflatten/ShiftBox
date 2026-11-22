package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestActivatePostgresReplacement verifies a native restore becomes usable
// only in recovery and uses the new role configuration.
func TestActivatePostgresReplacement(t *testing.T) {
	sourceURI, sourceDex := os.Getenv("SHIFTBOX_TEST_BACKUP_APP_URI"), os.Getenv("SHIFTBOX_TEST_BACKUP_DEX_URI")
	targetURI, targetDex := os.Getenv("SHIFTBOX_TEST_RESTORE_APP_URI"), os.Getenv("SHIFTBOX_TEST_RESTORE_DEX_URI")
	ca := os.Getenv("SHIFTBOX_TEST_RESTORE_CA")
	if sourceURI == "" || sourceDex == "" || targetURI == "" || targetDex == "" || ca == "" {
		t.Skip("requires isolated PostgreSQL source and target databases")
	}
	ctx := t.Context()
	source, err := database.Open(ctx, database.Config{Profile: "postgres", PostgresURI: sourceURI, Schema: "shiftbox"})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := database.Migrate(ctx, source, "postgres"); err != nil {
		t.Fatal(err)
	}
	dex, err := sql.Open("pgx", sourceDex)
	if err != nil {
		t.Fatal("open disposable Dex source")
	}
	t.Cleanup(func() { dex.Close() })
	if _, err := dex.ExecContext(ctx, "CREATE TABLE dex.restore_probe (value integer)"); err != nil {
		t.Fatal("initialize disposable Dex source")
	}
	if _, err := dex.ExecContext(ctx, "INSERT INTO dex.restore_probe VALUES (42)"); err != nil {
		t.Fatal("seed disposable Dex source")
	}
	id := uuid.NewString()
	if _, err := source.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,
		ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','postgres','https://management.test','{workspace}.ide.test','{service}--{workspace}.preview.test','fixture','fixture','now','now',1)`, id); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "archived.json")
	data := []byte(`{"installationDir":"/original","dataProfile":"postgres","postgresURI":"postgres://old:old@old.test/original?sslmode=verify-full","managementOrigin":"https://management.test"}`)
	input := backup.ProtectedInputs{Configuration: configPath, InstallationKey: filepath.Join(root, "key"), GuestCA: filepath.Join(root, "ca")}
	for path, contents := range map[string][]byte{configPath: data, input.InstallationKey: make([]byte, 32), input.GuestCA: []byte("fixture")} {
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backup.StagePostgresCheckpoint(ctx, stage, sourceURI, sourceDex, input,
		func(context.Context) (func(), error) { return func() {}, nil }); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(root, "target-dex.json")
	if err := writeRestoreFile(profilePath, []byte(`{"dexUri":"`+targetDex+`","caFile":"`+ca+`"}`), os.O_CREATE|os.O_EXCL); err != nil {
		t.Fatal(err)
	}
	profile, err := readSetupPostgresProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	target := &postgresReplacement{database: database.PostgresSetupTarget{ApplicationURI: targetURI, DexURI: targetDex, Roots: profile.Roots}, caPath: ca}
	dest := filepath.Join(root, "replacement")
	if err := prepareRestoreConfiguration(stage, dest, "postgres", target, nil); err != nil {
		t.Fatal(err)
	}
	review, err := activateRestore(ctx, stage, dest, backup.ArchiveMetadata{InstallationID: id}, uuid.NewString(), target)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dest, "configuration.json"))
	if err != nil || cfg.InstallationDir != dest {
		t.Fatal("replacement configuration invalid")
	}
	uri, err := url.Parse(cfg.PostgresURI)
	expected, wantErr := url.Parse(targetURI)
	if err != nil || wantErr != nil || uri.Host != expected.Host || uri.Path != expected.Path ||
		uri.User.String() != expected.User.String() || uri.Query().Get("sslrootcert") != ca {
		t.Fatal("replacement configuration did not use the validated PostgreSQL endpoint and trust")
	}
	db, err := database.Open(ctx, database.Config{Profile: "postgres", PostgresURI: cfg.PostgresURI, Schema: "shiftbox"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, reviewState string
	if err := db.QueryRowContext(ctx, "SELECT i.state,r.state FROM installations i CROSS JOIN recovery_reviews r WHERE r.id=$1", review).Scan(&state, &reviewState); err != nil || state != "recovery" || reviewState != "identity_review" {
		t.Fatalf("published state = %q/%q, %v; want recovery and identity_review", state, reviewState, err)
	}
	for _, name := range []string{"application.dump", "dex.dump"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatalf("published dump %s: %v", name, err)
		}
	}
	identityURI, err := url.Parse(targetDex)
	if err != nil {
		t.Fatal("build restored Dex connection")
	}
	query := identityURI.Query()
	query.Set("sslrootcert", ca)
	identityURI.RawQuery = query.Encode()
	identity, err := sql.Open("pgx", identityURI.String())
	if err != nil {
		t.Fatal("open restored Dex schema")
	}
	t.Cleanup(func() { identity.Close() })
	var value int
	if err := identity.QueryRowContext(ctx, "SELECT value FROM dex.restore_probe").Scan(&value); err != nil || value != 42 {
		t.Fatalf("restored Dex probe value = %d, query succeeded = %t; want 42 and a successful query", value, err == nil)
	}
	var original string
	if err := source.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&original); err != nil || original != "active" {
		t.Fatal("source was modified")
	}
}
