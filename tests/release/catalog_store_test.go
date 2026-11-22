package release_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/release"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	_ "modernc.org/sqlite"
)

// TestCatalogPublication checks unique release IDs, an explicit validation
// transition, default selection, and preservation of existing release metadata.
func TestCatalogPublication(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			ctx := context.Background()
			db := openCatalogDatabase(t, profile)
			store := release.NewStore(db)
			now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
			root, manifest := artifactFixture(t)
			first, err := release.ValidateArtifact(ctx, root, bytes.NewReader(manifest))
			if err != nil {
				t.Fatal(err)
			}
			firstID, secondID := testsupport.ID(201), testsupport.ID(202)
			t.Cleanup(func() {
				if _, err := db.ExecContext(context.Background(), "DELETE FROM platform_artifacts WHERE id IN ($1,$2)", firstID, secondID); err != nil {
					t.Errorf("remove catalog fixtures: %v", err)
				}
			})
			if err := store.BeginImport(ctx, firstID, first, testsupport.ID(1), now); err != nil {
				t.Fatal(err)
			}
			if err := store.BeginImport(ctx, secondID, first, testsupport.ID(1), now); !errors.Is(err, release.ErrCatalogConflict) {
				t.Errorf("duplicate release ID = %v, want conflict", err)
			}
			if err := store.SelectDefault(ctx, firstID, []string{"1"}); !errors.Is(err, release.ErrCatalogConflict) {
				t.Errorf("select validating artifact = %v, want conflict", err)
			}
			if err := store.CompleteImport(ctx, firstID, first.ManifestSHA256, true); err != nil {
				t.Fatal(err)
			}
			if err := store.SelectDefault(ctx, firstID, []string{"1"}); err != nil {
				t.Fatal(err)
			}

			secondInput := bytes.Replace(manifest, []byte("ubuntu-reference-1"), []byte("ubuntu-reference-2"), 1)
			secondInput = bytes.Replace(secondInput, []byte(`"protocolVersion":"1"`), []byte(`"protocolVersion":"2"`), 1)
			second, err := release.ValidateArtifact(ctx, root, bytes.NewReader(secondInput))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BeginImport(ctx, secondID, second, testsupport.ID(1), now); err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteImport(ctx, secondID, first.ManifestSHA256, true); !errors.Is(err, release.ErrCatalogConflict) {
				t.Errorf("finish wrong manifest = %v, want conflict", err)
			}
			if err := store.CompleteImport(ctx, secondID, second.ManifestSHA256, true); err != nil {
				t.Fatal(err)
			}
			if err := store.SelectDefault(ctx, secondID, []string{"1"}); !errors.Is(err, release.ErrCatalogConflict) {
				t.Errorf("select incompatible protocol = %v, want conflict", err)
			}
			current, err := store.Default(ctx)
			if err != nil || current.ID != firstID {
				t.Fatalf("default after failed selection = %s, %v, want %s", current.ID, err, firstID)
			}
			if err := store.SelectDefault(ctx, secondID, []string{"1", "2"}); err != nil {
				t.Fatal(err)
			}
			current, err = release.NewStore(db).Default(ctx)
			if err != nil || current.ID != secondID || current.Artifact.ManifestSHA256 != second.ManifestSHA256 {
				t.Fatalf("reopened default = %+v, %v, want second release", current, err)
			}
			old, err := store.Get(ctx, firstID)
			if err != nil || old.IsDefault || old.Artifact.Manifest.ReleaseID != first.Manifest.ReleaseID {
				t.Errorf("prior release = %+v, %v, want retained immutable release", old, err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET release_id='modified' WHERE id=$1", firstID); err == nil {
				t.Error("database allowed mutation of a published release")
			}
			if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET is_default=1 WHERE id=$1", firstID); err == nil {
				t.Error("database allowed two defaults")
			}
			if _, err := db.ExecContext(ctx, "UPDATE platform_artifacts SET manifest_sha256=$1 WHERE id=$2", first.ManifestSHA256, secondID); err == nil {
				t.Error("database allowed mutation of a published manifest digest")
			}
		})
	}
}

func openCatalogDatabase(t *testing.T, profile string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	cfg := database.Config{Profile: profile, SQLitePath: filepath.Join(t.TempDir(), "catalog.db")}
	if profile == "postgres" {
		cfg.PostgresURI = os.Getenv("SHIFTBOX_TEST_POSTGRES_URI")
		if cfg.PostgresURI == "" {
			t.Skip("set SHIFTBOX_TEST_POSTGRES_URI for PostgreSQL")
		}
		cfg.Schema = "shiftbox_test"
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES($1,'owner@example.test','administrator','active','local','setup','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)
		ON CONFLICT(id) DO NOTHING`, testsupport.ID(1)); err != nil {
		t.Fatal(err)
	}
	return db
}
