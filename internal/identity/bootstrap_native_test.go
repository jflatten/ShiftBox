package identity_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	api "github.com/dexidp/dex/api/v2"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	_ "modernc.org/sqlite"
)

// verifyNativeBootstrap exercises durable intent through real Dex and one-time
// delivery; it is called only while the pinned authenticated fixture is running.
func verifyNativeBootstrap(t *testing.T, ctx context.Context, client api.DexClient) {
	t.Helper()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "bootstrap.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES($1,'bootstrap','ShiftBox','sqlite','https://127.0.0.1:8443','{workspace}.ide.test','{service}--{workspace}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`, installation); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	intent := app.AdministratorIntent{InstallationID: installation, UserID: "00000000-0000-4000-8000-000000000002", Email: "bootstrap@example.test", CreatedAt: time.Now()}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { _, err := app.PrepareAdministrator(ctx, tx, intent, cipher); return err }); err != nil {
		t.Fatal(err)
	}
	admin := identity.PasswordAdmin{Client: client}
	if err := app.ReconcileAdministrator(ctx, db, installation, admin); err != nil {
		t.Fatal(err)
	}
	password, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.VerifyPassword(ctx, &api.VerifyPasswordReq{Email: intent.Email, Password: password})
	if err != nil || !response.Verified {
		t.Fatal("delivered bootstrap password did not authenticate in native Dex")
	}
	if err := app.ReconcileAdministrator(ctx, db, installation, admin); err != nil {
		t.Fatal(err)
	}
	if repeated, err := app.ClaimTemporaryPassword(ctx, db, installation, cipher, time.Now()); err == nil || repeated != "" {
		t.Fatal("native bootstrap retry redisplayed password")
	}
}
