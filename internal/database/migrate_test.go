package database

import (
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCheckSchemaRejectsIncompleteFoundation verifies that startup detects an
// older development schema even when every migration is recorded as applied.
func TestCheckSchemaRejectsIncompleteFoundation(t *testing.T) {
	ctx := t.Context()
	for _, change := range []struct {
		name string
		sql  string
	}{
		{"missing operation authority", "ALTER TABLE operations DROP COLUMN originating_action"},
		{"missing audit table", "DROP TABLE audit_events"},
		{"missing bootstrap journal link", "ALTER TABLE bootstrap_administrator RENAME COLUMN operation_id TO old_operation_id"},
	} {
		t.Run(change.name, func(t *testing.T) {
			db, err := Open(ctx, Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "shiftbox.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := Migrate(ctx, db, "sqlite"); err != nil {
				t.Fatal(err)
			}
			if err := CheckSchema(ctx, db, "sqlite"); err != nil {
				t.Fatalf("CheckSchema(current) = %v, want success", err)
			}
			if _, err := db.ExecContext(ctx, change.sql); err != nil {
				t.Fatal(err)
			}
			if err := CheckSchema(ctx, db, "sqlite"); !errors.Is(err, ErrSchemaDrift) {
				t.Fatalf("CheckSchema with an incomplete foundation = %v, want schema drift", err)
			}
		})
	}
}
