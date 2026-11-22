package auth_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/testsupport"
)

// TestHelperCredentialUsesCurrentSession checks credential separation, the
// external authorization deadline, and revocation through the browser session.
func TestHelperCredentialUsesCurrentSession(t *testing.T) {
	db, store, grants := grantFixture(t)
	ctx := t.Context()
	now := grants[0].GrantedAt
	session, browserToken, err := auth.NewSession(testsupport.ID(50), grants[0].SubjectUserID, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reauthorized := now.Add(-3*time.Hour - 30*time.Minute)
	session.ExternalAuthorizedAt = &reauthorized
	var helperToken string
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
			VALUES($1,'active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{workspace}-{service}.preview.test','release','release',$2,$2,1)`,
			testsupport.ID(1), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET credential_mode='external' WHERE id=$1`, session.UserID); err != nil {
			return err
		}
		if err := store.SaveSession(ctx, tx, session); err != nil {
			return err
		}
		token, deadline, err := store.IssueHelperCredential(ctx, tx, session.ID, now)
		if err != nil {
			return err
		}
		helperToken = token
		if want := now.Add(30 * time.Minute); !deadline.Equal(want) {
			t.Errorf("IssueHelperCredential deadline = %v, want %v", deadline, want)
		}
		if _, err := store.Principal(ctx, tx, helperToken); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Principal(helper credential) = %v, want no browser session", err)
		}
		if _, _, err := store.HelperPrincipal(ctx, tx, browserToken, now); !errors.Is(err, auth.ErrHelperCredential) {
			t.Errorf("HelperPrincipal(browser token) = %v, want helper credential denial", err)
		}
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT credential_digest FROM helper_credentials WHERE approving_session_id=$1`, session.ID).Scan(&digest); err != nil {
			return err
		}
		if digest != auth.TokenDigest(helperToken) {
			t.Error("helper credential was not stored as a verifier digest")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		principal, deadline, err := store.HelperPrincipal(ctx, tx, helperToken, now.Add(time.Minute))
		if err != nil {
			return err
		}
		if principal.Session.ID != session.ID || principal.User.ID != session.UserID || !deadline.Equal(now.Add(30*time.Minute)) {
			t.Error("helper credential was not bound to the approving session and its deadline")
		}
		if _, _, err := store.HelperPrincipal(ctx, tx, helperToken, deadline); !errors.Is(err, auth.ErrHelperCredential) {
			t.Errorf("HelperPrincipal(at authorization deadline) = %v, want denial", err)
		}
		return store.RevokeSession(ctx, tx, session.ID, now.Add(2*time.Minute), "logout")
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, _, err := store.HelperPrincipal(ctx, tx, helperToken, now.Add(3*time.Minute)); !errors.Is(err, auth.ErrHelperCredential) {
			t.Errorf("HelperPrincipal(after logout) = %v, want denial", err)
		}
		if _, _, err := store.IssueHelperCredential(ctx, tx, session.ID, now.Add(3*time.Minute)); !errors.Is(err, auth.ErrHelperCredential) {
			t.Errorf("IssueHelperCredential(after logout) = %v, want denial", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
