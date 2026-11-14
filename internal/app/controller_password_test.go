package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

type passwordFixture struct {
	current     string
	verifyCalls int
	replace     func() error
}

func (p *passwordFixture) PasswordVerified(_ context.Context, _, password string) (bool, error) {
	p.verifyCalls++
	return password == p.current, nil
}

func (p *passwordFixture) ReplacePassword(_ context.Context, _, password string) error {
	if err := p.replace(); err != nil {
		return err
	}
	p.current = password
	return nil
}

// TestControllerPasswordChange checks current-password verification, CSRF,
// session revocation before the Dex write, and forced-change recovery on failure.
func TestControllerPasswordChange(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	const origin = "https://management.test"
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "installation.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := auth.NewStore(db)
	user := auth.User{ID: uuid.NewString(), PrimaryEmail: "member@test.invalid", Role: "member",
		MembershipState: "active", CredentialMode: "local", MustChangePassword: true}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := store.CreateUser(ctx, tx, user, "fixture", now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	signIn := func() string {
		t.Helper()
		session, token, err := auth.NewSession(uuid.NewString(), user.ID, "password_change", now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error { return store.SaveSession(ctx, tx, session) }); err != nil {
			t.Fatal(err)
		}
		return token
	}
	registry := &proxy.Registry{}
	closed := false
	_, err = registry.Register(proxy.Scope{UserID: user.ID}, func() error { return nil }, func() error {
		closed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fails := true
	passwords := &passwordFixture{current: "temporary-password-fixture", replace: func() error {
		var sessions int
		err := db.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL", user.ID).Scan(&sessions)
		if err != nil || sessions != 0 || !closed {
			t.Fatalf("at Dex write: sessions=%d closed=%v (%v), want zero sessions and closed connection", sessions, closed, err)
		}
		if fails {
			return errors.New("fixture write failed")
		}
		return nil
	}}
	handler := activePasswordHandler(db, origin, registry, passwords, clock)
	send := func(token, current string, csrf bool, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, origin+"/api/v1/session/password",
			strings.NewReader(`{"currentPassword":"`+current+`","newPassword":"replacement-password-fixture"}`))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		if csrf {
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || strings.Contains(w.Body.String(), "password-fixture") {
			t.Fatalf("password change status = %d, want %d without password echo", w.Code, want)
		}
		return w
	}
	signIn()
	token := signIn()
	send(token, passwords.current, false, 403)
	if passwords.verifyCalls != 0 {
		t.Fatal("request without CSRF reached Dex")
	}
	send(token, "wrong-current", true, 403)
	send(token, passwords.current, true, 503)
	send(token, passwords.current, true, 401)
	var forced bool
	if err := db.QueryRowContext(ctx, "SELECT must_change_password FROM users WHERE id=$1", user.ID).Scan(&forced); err != nil || !forced {
		t.Fatalf("failed Dex write left forced-change=%v (%v), want true", forced, err)
	}
	fails = false
	w := send(signIn(), passwords.current, true, 204)
	if cookies := w.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatal("successful password change did not clear the session cookie")
	}
	if err := db.QueryRowContext(ctx, "SELECT must_change_password FROM users WHERE id=$1", user.ID).Scan(&forced); err != nil || forced ||
		passwords.current != "replacement-password-fixture" {
		t.Fatalf("completed password change left forced-change=%v (%v), want false and the new Dex credential", forced, err)
	}
}
