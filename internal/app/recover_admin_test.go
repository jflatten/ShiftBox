package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"golang.org/x/crypto/bcrypt"
)

type resetHashFixture func(context.Context, string, string, []byte) error

func (f resetHashFixture) ResetPasswordHash(ctx context.Context, email, user string, hash []byte) error {
	return f(ctx, email, user, hash)
}

// TestRecoverLocalAdministrator verifies recovery targets only an existing
// administrator and that a failed Dex write leaves old sessions revoked.
func TestRecoverLocalAdministrator(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "state.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: "https://management.test",
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test",
		Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := auth.NewStore(db)
	admin := auth.User{ID: uuid.NewString(), PrimaryEmail: "admin@example.test", Role: "administrator", MembershipState: "active", CredentialMode: "local"}
	member := auth.User{ID: uuid.NewString(), PrimaryEmail: "member@example.test", Role: "member", MembershipState: "active", CredentialMode: "local"}
	session, _, err := auth.NewSession(uuid.NewString(), admin.ID, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, user := range []auth.User{admin, member} {
			if _, err := store.CreateUser(ctx, tx, user, "fixture", now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation); err != nil {
			return err
		}
		return store.SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	fails, calls := true, 0
	var digest []byte
	dex := resetHashFixture(func(ctx context.Context, email, user string, hash []byte) error {
		calls++
		var revoked, forced bool
		err := db.QueryRowContext(ctx, `SELECT s.revoked_at IS NOT NULL,u.must_change_password FROM users u
			JOIN sessions s ON s.user_id=u.id WHERE u.id=$1 AND s.id=$2`, admin.ID, session.ID).Scan(&revoked, &forced)
		if email != admin.PrimaryEmail || user != admin.ID || err != nil || !revoked || !forced {
			t.Fatalf("Dex target=%s revoked=%t forced=%t (%v), want existing administrator with restricted access", user, revoked, forced, err)
		}
		if fails {
			return errors.New("fixture Dex write failed")
		}
		digest = append([]byte(nil), hash...)
		return nil
	})
	recover := func(email string) (string, error) {
		return recoverLocalAdministrator(ctx, db, installation, email, dex, now)
	}
	for _, email := range []string{member.PrimaryEmail, "missing@example.test"} {
		if value, err := recover(email); err == nil || value != "" || calls != 0 {
			t.Fatalf("recovery for %s reached Dex or issued a password", email)
		}
	}
	if value, err := recover(admin.PrimaryEmail); err == nil || value != "" {
		t.Fatal("failed Dex write returned a password or success")
	}
	fails = false
	password, err := recover(admin.PrimaryEmail)
	if err != nil || bcrypt.CompareHashAndPassword(digest, []byte(password)) != nil {
		t.Fatal("successful recovery did not return the credential sent to Dex")
	}
	var users, finished int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM users),
		(SELECT count(*) FROM audit_events WHERE actor_type='local_recovery' AND action='user.recover_admin' AND outcome='allowed' AND resource_id=$1)`, admin.ID).
		Scan(&users, &finished); err != nil || users != 2 || finished != 1 {
		t.Fatalf("recovery left %d users and %d completion audits (%v), want 2 and 1", users, finished, err)
	}
}
