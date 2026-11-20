package contract_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestDatabaseTransactions ensures a failed management mutation leaves no partial write.
func TestDatabaseTransactions(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		// Run the same failure contract on both supported persistence profiles.
		t.Run(profile, func(t *testing.T) {
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "state.db")}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				cfg.Schema = "shiftbox_test"
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL parity requires SHIFTBOX_TEST_POSTGRES_URI and owned shiftbox_test schema")
				}
			}
			ctx := context.Background()
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(ctx, "CREATE TEMPORARY TABLE transaction_fixture (id INTEGER PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected failure after first write")
			err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "INSERT INTO transaction_fixture VALUES (1)"); err != nil {
					return err
				}
				return failure
			})
			if !errors.Is(err, failure) {
				t.Fatalf("Transaction(failure) = %v, want injected error", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transaction_fixture").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("failed transaction retained %d rows, want 0", count)
			}
		})
	}
}

// TestFoundationRejectsConflictingIntent ensures retries cannot create two durable operations.
func TestFoundationRejectsConflictingIntent(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO operations (id, type, generation, requested_by, request_key, status, created_at, version) VALUES (?, 'provision', 1, 'actor', 'same-request', ?, '2026-11-13T00:00:00Z', 1)`
	if _, err := db.ExecContext(ctx, insert, "one", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, insert, "two", "pending"); err == nil {
		t.Error("duplicate idempotency key accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO operations (id, type, generation, requested_by, request_key, status, created_at, version) VALUES ('three', 'provision', 1, 'actor', 'other', 'invented', '2026-11-13T00:00:00Z', 1)`); err == nil {
		t.Error("unknown operation state accepted")
	}
}

// TestMigrationProfiles advances both dialects to the same durable foundation version.
func TestMigrationProfiles(t *testing.T) {
	versions := make(map[string]int64)
	for _, profile := range []string{"sqlite", "postgres"} {
		// Replaying migrations after a restart must preserve the installed version.
		t.Run(profile, func(t *testing.T) {
			cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "state.db"), Schema: "shiftbox_test"}
			if profile == "postgres" {
				cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
				if cfg.PostgresURI == "" {
					t.Skip("PostgreSQL fixture not configured")
				}
			}
			ctx := context.Background()
			db, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			for range 2 {
				if err := database.Migrate(ctx, db, profile); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.CheckSchema(ctx, db, profile); err != nil {
				t.Fatalf("CheckSchema(%s) after migration = %v, want compatible schema", profile, err)
			}
			// Both persistence profiles must install the scoped helper table.
			var helperCredentials int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(credential_digest) FROM helper_credentials").Scan(&helperCredentials); err != nil {
				t.Fatalf("helper credential migration (%s) = %v, want installed table", profile, err)
			}
			version, err := database.Version(ctx, db, profile)
			if err != nil || version < 1 {
				t.Fatalf("Version(%s) = %d, %v; want installed foundation", profile, version, err)
			}
			versions[profile] = version
			if err := database.Rollback(ctx, db, profile); !errors.Is(err, database.ErrRecoveryRequired) {
				t.Errorf("Rollback(%s) = %v, want guarded recovery requirement", profile, err)
			}
		})
	}
	if postgres, ok := versions["postgres"]; ok && postgres != versions["sqlite"] {
		t.Errorf("schema versions differ: postgres=%d sqlite=%d", postgres, versions["sqlite"])
	}
}

// TestPostgresRejectsUnownedSchema checks actual server ownership before any migration.
func TestPostgresRejectsUnownedSchema(t *testing.T) {
	uri := os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
	if uri == "" {
		t.Skip("PostgreSQL fixture not configured")
	}
	if db, err := database.Open(context.Background(), database.Config{Profile: "postgres", PostgresURI: uri, Schema: "pg_catalog"}); err == nil {
		db.Close()
		t.Error("Open(system-owned schema) succeeded, want ownership denial")
	}
}

// TestPostgresOwnershipFailsBeforeMigration refuses insecure or unowned production targets.
func TestPostgresOwnershipFailsBeforeMigration(t *testing.T) {
	for _, cfg := range []database.Config{
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=disable", Schema: "shiftbox"},
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=verify-full", Schema: "public"},
		{Profile: "postgres", PostgresURI: "postgres://secret-password@localhost/db?sslmode=verify-full", Schema: "dex"},
	} {
		if db, err := database.Open(context.Background(), cfg); err == nil {
			db.Close()
			t.Error("Open(unsafe postgres config) succeeded, want validation failure")
		} else if strings.Contains(err.Error(), "secret-password") {
			t.Error("Open error disclosed database credentials")
		}
	}
}
