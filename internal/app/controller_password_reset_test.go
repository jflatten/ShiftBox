package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"golang.org/x/crypto/bcrypt"
)

// TestControllerPasswordReset verifies that authorization, revocation and
// audit surround the Dex write and that an old request cannot issue a second password.
func TestControllerPasswordReset(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	const origin = "https://management.test"
	db, installation, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "installation.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := auth.NewStore(db)
	admin, member, external := uuid.NewString(), uuid.NewString(), uuid.NewString()
	sessions := make(map[string]auth.Session)
	tokens := make(map[string]string)
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		for _, user := range []auth.User{
			{ID: admin, PrimaryEmail: "admin@example.test", Role: "administrator", MembershipState: "active", CredentialMode: "local"},
			{ID: member, PrimaryEmail: "member@example.test", Role: "member", MembershipState: "active", CredentialMode: "local"},
			{ID: external, PrimaryEmail: "external@example.test", Role: "member", MembershipState: "active", CredentialMode: "external"},
		} {
			if _, err := store.CreateUser(ctx, tx, user, "fixture", now); err != nil {
				return err
			}
			session, token, err := auth.NewSession(uuid.NewString(), user.ID, "normal", now, time.Hour)
			if err != nil {
				return err
			}
			sessions[user.ID], tokens[user.ID] = session, token
			if err := store.SaveSession(ctx, tx, session); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", installation)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	grant := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO origin_grants (id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,permission_basis,expires_at)
		VALUES ($1,$2,$3,$4,$5,1,'ide','https://workspace.ide.test','workspace.connect_owner',$6)`,
		grant, strings.Repeat("a", 64), member, sessions[member].ID, uuid.NewString(), now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	closed := false
	if _, err := registry.Register(proxy.Scope{UserID: member}, func() error { return nil }, func() error {
		closed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var dexHash []byte
	dex := resetHashFixture(func(_ context.Context, email, subject string, hash []byte) error {
		calls++
		var forced, revoked, grantRevoked bool
		err := db.QueryRowContext(ctx, `SELECT u.must_change_password,s.revoked_at IS NOT NULL,g.revoked_at IS NOT NULL
			FROM users u JOIN sessions s ON s.user_id=u.id JOIN origin_grants g ON g.session_id=s.id WHERE g.id=$1`, grant).
			Scan(&forced, &revoked, &grantRevoked)
		if err != nil || !forced || !revoked || !grantRevoked || !closed || email != "member@example.test" || subject != member {
			t.Fatal("Dex reset began without the expected identity, forced change, and access revocation", err)
		}
		if calls == 1 {
			return errors.New("private Dex fixture detail")
		}
		dexHash = append([]byte(nil), hash...)
		return nil
	})
	handler := resetPasswordHandler(db, origin, registry, dex, func() time.Time { return now }, &sync.Mutex{})
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/admin/users/{userId}/password-reset", &handler)
	send := func(actor, target, version string, csrf bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/admin/users/"+target+"/password-reset", nil)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: tokens[actor]})
		r.Header.Set("Origin", origin)
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", auth.CSRFToken(tokens[actor]))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("password reset status = %d, want %d and no-store", w.Code, status)
		}
		return w
	}
	send(member, member, `"1"`, true, 403)
	send(admin, member, `"1"`, false, 403)
	send(admin, member, "", true, 428)
	send(admin, uuid.NewString(), `"1"`, true, 404)
	send(admin, external, `"1"`, true, 409)
	send(admin, admin, `"1"`, true, 409)
	if calls != 0 {
		t.Fatal("ineligible requests reached Dex")
	}

	failure := send(admin, member, `"1"`, true, 503)
	if strings.Contains(failure.Body.String(), "private Dex") || strings.Contains(failure.Body.String(), "temporaryPassword") {
		t.Fatal("failed Dex reset exposed private response data")
	}
	send(admin, member, `"1"`, true, 412)
	success := send(admin, member, `"2"`, true, 200)
	var result struct {
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &result); err != nil ||
		bcrypt.CompareHashAndPassword(dexHash, []byte(result.TemporaryPassword)) != nil {
		t.Fatal("reset response did not contain the newly installed credential")
	}
	send(admin, member, `"2"`, true, 412)
	send(member, member, `"3"`, true, 401)
	var started, allowed, leaked int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM audit_events WHERE action='user.reset_password' AND outcome='started'),
		(SELECT count(*) FROM audit_events WHERE action='user.reset_password' AND outcome='allowed' AND actor_id=$1 AND resource_id=$2),
		(SELECT count(*) FROM audit_events WHERE metadata_json LIKE $3)`, admin, member, "%"+result.TemporaryPassword+"%").
		Scan(&started, &allowed, &leaked); err != nil || calls != 2 || started != 2 || allowed != 1 || leaked != 0 {
		t.Fatalf("reset completion: Dex writes=%d started=%d allowed=%d leaked=%d (%v), want 2/2/1/0", calls, started, allowed, leaked, err)
	}
}
