package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// TestBackupCommandUsesController verifies archive creation through the private
// listener, passphrase confirmation, and success only after durable verification.
func TestBackupCommandUsesController(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	db, _, err := database.OpenBootstrap(ctx, filepath.Join(dir, "db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "Fixture", Origin: "https://management.test",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "fixture", MinGuestRelease: "fixture", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active'"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, "SELECT id FROM installations").Scan(&id); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(map[string]any{"installationDir": dir, "dataProfile": "sqlite", "sqlitePath": filepath.Join(dir, "db"), "managementOrigin": "https://management.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	service := backup.NewService(db, operation.NewJournal(db, time.Now, uuid.NewString), backup.ArchiveMetadata{InstallationID: id, Release: "fixture", Profile: "sqlite", Schema: 19})
	calls := 0
	listener, err := listenLocalBackup(dir, service, func(_ context.Context, stage string) (map[string]string, error) {
		calls++
		files := map[string]string{}
		for _, name := range []string{"application.db", "dex.db", "configuration.json", "installation.key", "guest-ca.json"} {
			path := filepath.Join(stage, name)
			if err := os.WriteFile(path, []byte(name), 0600); err != nil {
				return nil, err
			}
			files[name] = path
		}
		return files, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- listener.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	const password = "test-only confirmed password"
	output := filepath.Join(dir, "management.age")
	args := []string{"--json", "--output", output, "--config", configPath, "--password-stdin"}
	var result, status bytes.Buffer
	err = runBackup(ctx, args, strings.NewReader(password+"\ndifferent password\n"), &result, &status)
	var exit cliExit
	if !errors.As(err, &exit) || exit.code != 2 || strings.Contains(result.String()+status.String(), password) {
		t.Fatalf("mismatched passwords = %v; want exit 2 with sanitized output", err)
	}
	result.Reset()
	if err := runBackup(ctx, args, strings.NewReader(password+"\n"+password+"\n"), &result, &status); err != nil {
		t.Fatalf("backup = %v %s", err, result.String())
	}
	var envelope struct {
		Result struct{ OperationID, ManifestDigest string }
	}
	if err := json.Unmarshal(result.Bytes(), &envelope); err != nil || envelope.Result.OperationID == "" || envelope.Result.ManifestDigest == "" {
		t.Fatalf("backup result missing verification evidence: %v", err)
	}
	var state string
	if err := db.QueryRowContext(ctx, "SELECT state FROM backup_records WHERE operation_id=$1", envelope.Result.OperationID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("backup returned before durable completion: %q, %v", state, err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("capture ran %d times; want one", calls)
	}
}
