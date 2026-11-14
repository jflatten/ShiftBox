package database_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestOpenBootstrapResumesInstallation checks that reopening local setup preserves
// the durable identity and progress, and refuses to reopen an active installation.
func TestOpenBootstrapResumesInstallation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bootstrap.db")
	seed := database.BootstrapSeed{
		ID:           "00000000-0000-4000-8000-000000000001",
		Organization: "ShiftBox", Origin: "https://127.0.0.1:8443",
		IDEPattern: "{workspace}.ide.example.com", PreviewPattern: "{service}--{workspace}.preview.example.com",
		Release: "test-release", MinGuestRelease: "previous-release", Now: time.Now(),
	}
	db, id, err := database.OpenBootstrap(ctx, path, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if id != seed.ID {
		t.Fatalf("first bootstrap ID = %s, want %s", id, seed.ID)
	}
	if _, err := db.Exec("UPDATE installations SET state='configuring', version=2"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	seed.ID = "00000000-0000-4000-8000-000000000002"
	seed.Origin = "https://127.0.0.1:8444"
	db, resumed, err := database.OpenBootstrap(ctx, path, seed)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, origin string
	var version int
	if err := db.QueryRow("SELECT state, management_origin, version FROM installations").Scan(&state, &origin, &version); err != nil {
		t.Fatal(err)
	}
	if resumed != id || state != "configuring" || origin != "https://127.0.0.1:8443" || version != 2 {
		t.Fatalf("reopening bootstrap changed stored identity or progress: %s %s %s %d", resumed, state, origin, version)
	}
	if _, err := db.Exec("UPDATE installations SET state='active'"); err != nil {
		t.Fatal(err)
	}
	rejected, _, err := database.OpenBootstrap(ctx, path, seed)
	if rejected != nil {
		rejected.Close()
		t.Fatal("active installation returned a bootstrap database")
	}
	if !errors.Is(err, database.ErrBootstrapUnavailable) {
		t.Fatalf("opening active installation for setup = %v, want bootstrap unavailable", err)
	}
}
