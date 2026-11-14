package proxy

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jflatten/ShiftBox/internal/database"
	_ "modernc.org/sqlite"
)

// TestOriginGrantExchange verifies exact binding, durable one-use redemption,
// and that raw browser credentials never become database keys.
func TestOriginGrantExchange(t *testing.T) {
	ctx := t.Context()
	cfg := database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "grants.db")}
	if uri := os.Getenv("SHIFTBOX_TEST_ORIGIN_POSTGRES_URI"); uri != "" {
		cfg = database.Config{Profile: "postgres", PostgresURI: uri, Schema: "shiftbox_test"}
	}
	db, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, cfg.Profile); err != nil {
		t.Fatal(err)
	}
	scope := OriginScope{WorkspaceID: "workspace", Generation: 4, Service: "ide", Origin: "https://workspace.ide.test"}
	now := time.Now().UTC()
	var token string
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		token, _, err = IssueOriginGrant(ctx, tx, OriginAuthority{Scope: scope, UserID: "member", SessionID: "session",
			Permission: "workspace.connect_owner"}, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := db.QueryRow("SELECT token_digest FROM origin_grants").Scan(&digest); err != nil || digest == token || len(digest) != 64 {
		t.Fatalf("stored exchange credential is not a digest, err %v", err)
	}
	exchange := func(scope OriginScope, stamp time.Time) error {
		return database.Transaction(ctx, db, func(tx *sql.Tx) error {
			_, err := ConsumeOriginGrant(ctx, tx, scope, token, stamp)
			return err
		})
	}
	sibling := scope
	sibling.Origin = "https://workspace.preview.test"
	if err := exchange(sibling, now); !errors.Is(err, ErrOriginGrant) {
		t.Errorf("exchange on sibling origin = %v, want denial", err)
	}
	if err := exchange(scope, now.Add(time.Minute)); !errors.Is(err, ErrOriginGrant) {
		t.Errorf("exchange at expiry = %v, want denial", err)
	}
	var issued OriginExchange
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var err error
		issued, err = ConsumeOriginGrant(ctx, tx, scope, token, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if issued.Cookie == "" || issued.Cookie == token {
		t.Fatal("exchange did not create a distinct browser credential")
	}
	if err := exchange(scope, now); !errors.Is(err, ErrOriginGrant) {
		t.Errorf("replayed exchange = %v, want denial", err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		got, err := ReadOriginCookie(ctx, tx, scope, issued.Cookie, now)
		if err != nil || got != issued.Authority {
			t.Errorf("read cookie returned %+v, %v, want %+v", got, err, issued.Authority)
		}
		stale := scope
		stale.Generation--
		if _, err := ReadOriginCookie(ctx, tx, stale, issued.Cookie, now); !errors.Is(err, ErrOriginGrant) {
			t.Errorf("read cookie for stale generation = %v, want denial", err)
		}
		if _, err := ReadOriginCookie(ctx, tx, scope, issued.Cookie, issued.ExpiresAt); !errors.Is(err, ErrOriginGrant) {
			t.Errorf("read expired cookie = %v, want denial", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
