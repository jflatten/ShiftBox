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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"golang.org/x/crypto/bcrypt"
)

type testPasswordEnsure func(context.Context, string, string, []byte) error

func (f testPasswordEnsure) Ensure(ctx context.Context, email, id string, hash []byte) error {
	return f(ctx, email, id, hash)
}

// TestCreateLocalUser checks that an interrupted Dex write can resume at the
// same identity and that subsequent requests never redisplay the credential.
func TestCreateLocalUser(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	const origin = "https://management.test"
	db, id, err := database.OpenBootstrap(ctx, filepath.Join(t.TempDir(), "users.db"), database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "ShiftBox", Origin: origin, IDEPattern: "{workspace}.ide.test",
		PreviewPattern: "{service}--{workspace}.preview.test", Release: "test", MinGuestRelease: "test", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db)
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	signIn := func(role string) string {
		t.Helper()
		user := auth.User{ID: uuid.NewString(), PrimaryEmail: role + "@test.invalid", Role: role, MembershipState: "active", CredentialMode: "local"}
		session, token, err := auth.NewSession(uuid.NewString(), user.ID, "normal", now, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
			if _, err := store.CreateUser(ctx, tx, user, "fixture", now); err != nil {
				return err
			}
			return store.SaveSession(ctx, tx, session)
		}); err != nil {
			t.Fatal(err)
		}
		return token
	}
	admin, member := signIn("administrator"), signIn("member")
	var dexID string
	var dexHash []byte
	calls := 0
	dex := testPasswordEnsure(func(ctx context.Context, email, userID string, hash []byte) error {
		calls++
		var visible int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id=$1", userID).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("membership at Dex create = %d rows (%v), want zero", visible, err)
		}
		if email != "new@test.invalid" {
			t.Fatalf("Dex email = %q, want normalized local email", email)
		}
		if calls == 1 {
			dexID, dexHash = userID, append([]byte(nil), hash...)
			return errors.New("fixture lost Dex response")
		}
		if userID != dexID {
			t.Fatalf("retried Dex ID = %q, want %q", userID, dexID)
		}
		return nil
	})
	state := &ControllerState{DB: db, Installation: ActiveInstallation{InstallationID: id,
		Settings: config.InstallationSettings{ManagementOrigin: origin}}, cipher: cipher}
	send := func(token, email, role string, want int) *httptest.ResponseRecorder {
		t.Helper()
		handler := state.createLocalUserHandler(dex, clock)
		body, err := json.Marshal(map[string]string{"email": email, "role": role})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/admin/users", strings.NewReader(string(body)))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || (want != 201 && strings.Contains(w.Body.String(), "temporaryPassword")) {
			t.Fatalf("create local user = %d, want %d and one-time delivery only on success", w.Code, want)
		}
		return w
	}
	send(member, "new@test.invalid", "administrator", 403)
	send(admin, "invalid email", "member", 400)
	send(admin, "New@test.invalid", "member", 503)
	send(admin, "new@test.invalid", "administrator", 409)
	res := send(admin, "new@test.invalid", "member", 201)
	var created struct {
		User              auth.User
		TemporaryPassword string
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil || created.User.ID != dexID ||
		created.User.Role != "member" || created.User.MembershipState != "active" || !created.User.MustChangePassword ||
		bcrypt.CompareHashAndPassword(dexHash, []byte(created.TemporaryPassword)) != nil ||
		res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response did not deliver the original Dex credential exactly once for a forced-change local account")
	}
	send(admin, " NEW@test.invalid ", "member", 409)
	var pending, auditEvents int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM local_user_creations),
		(SELECT count(*) FROM audit_events WHERE action='user.create' AND resource_id=$1)`, dexID).Scan(&pending, &auditEvents); err != nil ||
		pending != 0 || auditEvents != 1 || calls != 2 {
		t.Fatalf("local creation left %d pending, %d audits, %d Dex calls (%v), want 0/1/2", pending, auditEvents, calls, err)
	}
}
