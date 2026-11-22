package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/testsupport"
)

// TestActiveGrantsUsesExactCurrentScope excludes revoked grants and grants for another user or resource.
func TestActiveGrantsUsesExactCurrentScope(t *testing.T) {
	db, store, grants := grantFixture(t)
	ctx := context.Background()
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		got, err := store.ActiveGrants(ctx, tx, grants[0].SubjectUserID, "workspace", grants[0].ResourceID)
		if err != nil {
			return err
		}
		if len(got) != 1 {
			t.Fatalf("ActiveGrants(exact scope) count = %d, want 1", len(got))
		}
		if got[0] != grants[0] {
			t.Errorf("ActiveGrants(exact scope) = %+v, want %+v", got[0], grants[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrantRevocationIsTransactional preserves permission on rollback and removes it only after durable revocation.
func TestGrantRevocationIsTransactional(t *testing.T) {
	db, store, grants := grantFixture(t)
	ctx := context.Background()
	now := grants[0].GrantedAt.Add(time.Hour)
	principal := auth.Principal{
		User:              auth.User{ID: grants[0].SubjectUserID, Role: "administrator", MembershipState: "active", CredentialMode: "local"},
		Session:           auth.Session{UserID: grants[0].SubjectUserID, Mode: "normal", AuthenticatedAt: now, ExpiresAt: now.Add(time.Hour)},
		InstallationState: "active",
	}
	resource := auth.Resource{Type: "workspace", ID: grants[0].ResourceID, OwnerID: testsupport.ID(99), Eligible: true}
	failure := errors.New("audit insert failed")
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := store.RevokeGrant(ctx, tx, grants[0].ID, now); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("RevokeGrant rollback = %v, want injected failure", err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		active, err := store.ActiveGrants(ctx, tx, grants[0].SubjectUserID, "workspace", grants[0].ResourceID)
		if err != nil {
			return err
		}
		if len(active) != 1 {
			t.Errorf("ActiveGrants(after rollback) = %d, want 1", len(active))
		}
		decision := auth.Authorize(principal, "workspace.connect_admin", resource, active, now)
		if !decision.Allowed || !decision.AuditRequired || decision.OwnerContext != resource.OwnerID {
			t.Errorf("Authorize(active stored grant) = %+v, want audited access in workspace owner context", decision)
		}
		return store.RevokeGrant(ctx, tx, grants[0].ID, now)
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		// A retry must preserve the first revocation timestamp for audit correlation.
		if err := store.RevokeGrant(ctx, tx, grants[0].ID, now.Add(time.Hour)); err != nil {
			return err
		}
		active, err := store.ActiveGrants(ctx, tx, grants[0].SubjectUserID, "workspace", grants[0].ResourceID)
		if err != nil {
			return err
		}
		if len(active) != 0 {
			t.Errorf("ActiveGrants(after revocation) = %d, want 0", len(active))
		}
		if decision := auth.Authorize(principal, "workspace.connect_admin", resource, active, now); decision.Allowed {
			t.Error("Authorize(revoked stored grant) allowed administrator connection, want denial")
		}
		var revoked string
		if err := tx.QueryRowContext(ctx, "SELECT revoked_at FROM resource_grants WHERE id=$1", grants[0].ID).Scan(&revoked); err != nil {
			return err
		}
		got, err := time.Parse(time.RFC3339Nano, revoked)
		if err != nil {
			return err
		}
		if !got.Equal(now) {
			t.Errorf("RevokeGrant retry timestamp = %v, want %v", got, now)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func grantFixture(t *testing.T) (*sql.DB, *auth.Store, []auth.ResourceGrant) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "grants.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	first, second, workspace := testsupport.ID(1), testsupport.ID(2), testsupport.ID(3)
	grants := []auth.ResourceGrant{
		{ID: testsupport.ID(10), SubjectUserID: first, ResourceType: "workspace", ResourceID: workspace, Action: "workspace.connect_admin", GrantedBy: second, GrantedAt: now},
		{ID: testsupport.ID(11), SubjectUserID: first, ResourceType: "workspace", ResourceID: workspace, Action: "workspace.connect_admin", GrantedBy: second, GrantedAt: now, RevokedAt: &now},
		{ID: testsupport.ID(12), SubjectUserID: second, ResourceType: "workspace", ResourceID: workspace, Action: "workspace.connect_admin", GrantedBy: second, GrantedAt: now},
		{ID: testsupport.ID(13), SubjectUserID: first, ResourceType: "workspace", ResourceID: testsupport.ID(4), Action: "workspace.connect_admin", GrantedBy: second, GrantedAt: now},
		{ID: testsupport.ID(14), SubjectUserID: first, ResourceType: "template", ResourceID: workspace, Action: "template.view", GrantedBy: second, GrantedAt: now},
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, id := range []string{first, second} {
			if _, err := store.CreateUser(ctx, tx, auth.User{ID: id, PrimaryEmail: id + "@example.test", Role: "administrator", MembershipState: "active", CredentialMode: "local"}, "setup", now); err != nil {
				return err
			}
		}
		for _, grant := range grants {
			if err := store.Grant(ctx, tx, grant); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return db, store, grants
}
