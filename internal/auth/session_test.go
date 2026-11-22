package auth_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	_ "modernc.org/sqlite"
)

// TestPrincipalReadsCurrentAuthority makes role changes, disablement and installation gates visible without issuing a new cookie.
func TestPrincipalReadsCurrentAuthority(t *testing.T) {
	db, store, grants := grantFixture(t)
	ctx := context.Background()
	now := grants[0].GrantedAt
	session, token, err := auth.NewSession(testsupport.ID(50), grants[0].SubjectUserID, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version) VALUES('00000000-0000-4000-8000-000000000001','active','ShiftBox','sqlite','https://management.test','{workspace}.ide.test','{workspace}-{service}.preview.test','release','release','2026-11-14T00:00:00Z','2026-11-14T00:00:00Z',1)`); err != nil {
			return err
		}
		return store.SaveSession(ctx, tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		principal, err := store.Principal(ctx, tx, token)
		if err != nil {
			return err
		}
		if principal.User.ID != session.UserID || principal.Session.ID != session.ID || principal.InstallationState != "active" || principal.ExternalAuthInterval != 4*time.Hour {
			t.Errorf("Principal(cookie) = %+v, want current user/session/active installation/four-hour interval", principal)
		}
		if !auth.Authorize(principal, "infrastructure.view", auth.Resource{}, nil, now).Allowed {
			t.Error("current administrator was denied infrastructure view")
		}
		if _, err := store.Principal(ctx, tx, "unknown-cookie"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Principal(unknown) = %v, want no rows", err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET role='member', membership_state='disabled' WHERE id=$1`, session.UserID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		principal, err := store.Principal(ctx, tx, token)
		if err != nil {
			return err
		}
		if principal.User.Role != "member" || principal.User.MembershipState != "disabled" {
			t.Errorf("Principal after membership change = %+v, want disabled member", principal.User)
		}
		if auth.Authorize(principal, "session.view", auth.Resource{}, nil, now).Allowed {
			t.Error("disabled membership retained session access")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSessionStoresOnlyDigests prevents a database read from yielding a usable browser credential.
func TestSessionStoresOnlyDigests(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	first, token, err := auth.NewSession(testsupport.ID(1), "alice", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := auth.NewSession(testsupport.ID(2), "alice", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token == other || len(token) < 32 {
		t.Error("session tokens are reused or too short")
	}
	if first.TokenDigest != auth.TokenDigest(token) || first.TokenDigest == token {
		t.Error("session does not store a one-way token digest")
	}
	csrf := auth.CSRFToken(token)
	if first.CSRFDigest != auth.TokenDigest(csrf) || csrf == token {
		t.Error("CSRF is not independently bound to the session")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), csrf) {
		t.Error("session serialization exposes credentials")
	}
	if auth.ValidCSRF(first, auth.CSRFToken(other)) {
		t.Error("CSRF from another session accepted")
	}
	if !auth.ValidCSRF(first, csrf) {
		t.Error("session-bound CSRF rejected")
	}
}

// TestRevocationCommitsBeforeClosing allows connection teardown to observe the revoked authority.
func TestRevocationCommitsBeforeClosing(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "revoke.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	userID := testsupport.ID(1)
	session, token, err := auth.NewSession(testsupport.ID(2), userID, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := store.CreateUser(ctx, tx, auth.User{ID: userID, PrimaryEmail: "alice@example.test", Role: "member", MembershipState: "active", CredentialMode: "local"}, "setup", now); err != nil {
			return err
		}
		return store.SaveSession(ctx, tx, session)
	})
	if err != nil {
		t.Fatal(err)
	}
	var registry proxy.Registry
	closed := false
	_, err = registry.Register(proxy.Scope{UserID: userID, SessionID: session.ID}, func() error { return nil }, func() error {
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return database.Transaction(readCtx, db, func(tx *sql.Tx) error {
			stored, err := store.Session(readCtx, tx, token)
			if err != nil {
				return err
			}
			if stored.RevokedAt == nil {
				return errors.New("close happened before revocation commit")
			}
			closed = true
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.RevokeAndClose(cancelled, session.ID, now, "session.logout", &registry); err == nil {
		t.Error("cancelled revocation succeeded")
	}
	if closed {
		t.Error("connection closed before failed transaction committed")
	}
	if err := store.RevokeAndClose(ctx, session.ID, now, "session.logout", &registry); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Error("revocation returned before connection closed")
	}
}

// TestRevokeUserSessions commits revocation for every current session and
// preserves earlier revocations and unrelated sessions across retries.
func TestRevokeUserSessions(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "offboarding.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	target, other := testsupport.ID(1), testsupport.ID(2)
	owners := []string{target, target, target, other}
	tokens := make([]string, len(owners))
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for id, email := range map[string]string{target: "member@example.test", other: "other@example.test"} {
			if _, err := store.CreateUser(ctx, tx, auth.User{ID: id, PrimaryEmail: email, Role: "member", MembershipState: "active", CredentialMode: "local"}, "setup", now); err != nil {
				return err
			}
		}
		for i, id := range owners {
			session, token, err := auth.NewSession(testsupport.ID(10+uint32(i)), id, "normal", now, time.Hour)
			if err != nil {
				return err
			}
			tokens[i] = token
			if err := store.SaveSession(ctx, tx, session); err != nil {
				return err
			}
		}
		return store.RevokeSession(ctx, tx, testsupport.ID(12), now, "session.logout")
	}); err != nil {
		t.Fatal(err)
	}
	locallyRejected := errors.New("workspace stop intent could not be queued")
	disableAt := now.Add(time.Minute)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := store.RevokeUserSessions(ctx, tx, target, disableAt, "membership_disabled"); err != nil {
			return err
		}
		return locallyRejected
	}); !errors.Is(err, locallyRejected) {
		t.Fatalf("rolled-back offboarding = %v, want injected stop admission failure", err)
	}
	check := func(index int, want string, at time.Time) {
		t.Helper()
		err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			session, err := store.Session(ctx, tx, tokens[index])
			if err != nil {
				return err
			}
			switch {
			case at.IsZero() && session.RevokedAt != nil:
				t.Errorf("session %d unexpectedly revoked", index)
			case !at.IsZero() && (session.RevokedAt == nil || !session.RevokedAt.Equal(at) ||
				session.RevocationReason != want):
				t.Errorf("session %d revocation = %v, reason %v, want %v (%s)", index, session.RevokedAt, session.RevocationReason, at, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(0, "", time.Time{})
	for _, at := range []time.Time{disableAt, disableAt.Add(time.Minute)} {
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			return store.RevokeUserSessions(ctx, tx, target, at, "membership_disabled")
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(0, "membership_disabled", disableAt)
	check(1, "membership_disabled", disableAt)
	check(2, "session.logout", now)
	check(3, "", time.Time{})
}

// TestIdentityAndSessionPersistence keeps equal email profiles separate and persists only digests.
func TestIdentityAndSessionPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "auth.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	firstID, secondID := testsupport.ID(1), testsupport.ID(2)
	session, token, err := auth.NewSession(testsupport.ID(3), firstID, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, id := range []string{firstID, secondID} {
			user, err := store.CreateUser(ctx, tx, auth.User{ID: id, PrimaryEmail: "Person@Example.test", Role: "member", MembershipState: "active", CredentialMode: "external"}, "setup", now)
			if err != nil {
				return err
			}
			if user.PrimaryEmail != "person@example.test" {
				t.Errorf("normalized email = %q", user.PrimaryEmail)
			}
		}
		if err := store.BindIdentity(ctx, tx, auth.IdentityBinding{ID: testsupport.ID(4), UserID: firstID, Issuer: "https://issuer.test", Subject: "first-subject", ConnectorID: "oidc", EmailAtLink: "person@example.test", LinkedBy: "setup", LinkedAt: now}); err != nil {
			return err
		}
		if _, err := store.Identity(ctx, tx, "https://issuer.test", "second-subject", "oidc"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("unbound subject lookup = %v, want no identity", err)
		}
		if err := store.SaveSession(ctx, tx, session); err != nil {
			return err
		}
		stored, err := store.Session(ctx, tx, token)
		if err != nil {
			return err
		}
		if stored.TokenDigest == token || stored.TokenDigest != session.TokenDigest {
			t.Error("session persistence contains raw or wrong token")
		}
		return store.RevokeSession(ctx, tx, session.ID, now.Add(time.Minute), "session.logout")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		stored, err := store.Session(ctx, tx, token)
		if err != nil {
			return err
		}
		if stored.RevokedAt == nil {
			t.Error("session revocation was not durable")
		}
		user, err := store.Identity(ctx, tx, "https://issuer.test", "first-subject", "oidc")
		if err != nil {
			return err
		}
		if user.ID != firstID {
			t.Error("stable subject binding selected another same-email user")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGrantVocabularyRejectsInventedAuthority prevents arbitrary permission strings entering stores.
func TestGrantVocabularyRejectsInventedAuthority(t *testing.T) {
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
	err = database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return store.Grant(ctx, tx, auth.ResourceGrant{ID: testsupport.ID(1), Action: "workspace.superuser", ResourceType: "workspace"})
	})
	if !errors.Is(err, auth.ErrInvalidGrant) {
		t.Errorf("Grant(invented action) = %v, want invalid grant", err)
	}
}

// TestSessionRejectsInvalidLifetime prevents immediately expired or unknown-mode sessions.
func TestSessionRejectsInvalidLifetime(t *testing.T) {
	for _, duration := range []time.Duration{0, -time.Second} {
		if _, _, err := auth.NewSession(testsupport.ID(1), "alice", "normal", time.Now(), duration); err == nil {
			t.Errorf("NewSession(lifetime %v) succeeded", duration)
		}
	}
	if _, _, err := auth.NewSession(testsupport.ID(1), "alice", "invented", time.Now(), time.Hour); err == nil {
		t.Error("unknown session mode accepted")
	}
}
